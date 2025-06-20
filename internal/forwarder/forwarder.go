package forwarder

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"golang.org/x/time/rate"

	"github.com/zdev0x/go-realm/internal/config"
	"github.com/zdev0x/go-realm/internal/logger"
	"github.com/zdev0x/go-realm/internal/pool"
	"github.com/zdev0x/go-realm/internal/stats"
	"github.com/zdev0x/go-realm/internal/zerocopy"
	"github.com/zdev0x/go-realm/pkg/utils"
)

// Forwarder 表示一个端口转发器
type Forwarder struct {
	Endpoint        config.Endpoint
	Stats           *stats.StatsManager
	DNSCache        *lru.Cache
	Resolver        *net.Resolver
	DNSConfig       config.DNSConfig
	readTimeout     time.Duration
	writeTimeout    time.Duration
	idleTimeout     time.Duration
	connPool        *pool.ConnPool // 连接池
	ZeroCopyEnabled bool           // 是否启用零拷贝
	limiter         *rate.Limiter
	logger          *logger.AsyncLogger
}

// NewForwarder 创建一个新的转发器
func NewForwarder(endpoint config.Endpoint, cache *lru.Cache, resolver *net.Resolver, dnsConfig config.DNSConfig, logger *logger.AsyncLogger) *Forwarder {
	var limiter *rate.Limiter
	if endpoint.RateLimitBps > 0 {
		limiter = rate.NewLimiter(rate.Limit(endpoint.RateLimitBps), int(endpoint.RateLimitBps))
	}

	f := &Forwarder{
		Endpoint:     endpoint,
		Stats:        stats.NewStatsManager(endpoint.StatsEnabled),
		DNSCache:     cache,
		Resolver:     resolver,
		DNSConfig:    dnsConfig,
		readTimeout:  30 * time.Second,
		writeTimeout: 30 * time.Second,
		idleTimeout:  60 * time.Second,
		connPool:     pool.NewConnPool(endpoint.Remote, 10, resolver, dnsConfig),
		limiter:      limiter,
		logger:       logger,
	}

	return f
}

// Start 启动转发器
func (f *Forwarder) Start(network config.NetworkConfig) error {
	// 检查是否启用零拷贝
	if network.ZeroCopy {
		if zerocopy.IsZeroCopySupported() {
			f.ZeroCopyEnabled = true
			f.logger.Printf("零拷贝模式已启用: %s -> %s", f.Endpoint.Local, f.Endpoint.Remote)
		} else {
			f.logger.Printf("警告: 零拷贝不受支持，将使用标准转发: %s -> %s", f.Endpoint.Local, f.Endpoint.Remote)
		}
	}

	// 启动TCP转发
	if !network.NoTCP {
		if err := f.startTCP(); err != nil {
			return fmt.Errorf("启动TCP转发失败: %v", err)
		}
	}

	// 启动UDP转发
	if network.UseUDP {
		if err := f.startUDP(); err != nil {
			return fmt.Errorf("启动UDP转发失败: %v", err)
		}
	}

	return nil
}

// startTCP 启动TCP转发
func (f *Forwarder) startTCP() error {
	listener, err := net.Listen("tcp", f.Endpoint.Local)
	if err != nil {
		return fmt.Errorf("监听TCP端口失败: %v", err)
	}

	f.logger.Printf("TCP转发已启动: %s -> %s", f.Endpoint.Local, f.Endpoint.Remote)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				f.logger.Printf("接受连接失败: %v", err)
				time.Sleep(time.Second)
				continue
			}

			go f.handleTCPConn(conn)
		}
	}()

	return nil
}

// handleTCPConn 处理单个TCP连接
func (f *Forwarder) handleTCPConn(conn net.Conn) {
	clientAddr := conn.RemoteAddr().String()
	f.logger.Printf("新连接: %s -> %s -> %s", clientAddr, f.Endpoint.Local, f.Endpoint.Remote)

	// 增加连接计数
	f.Stats.IncrementConnections()
	defer f.Stats.DecrementActiveConnections()

	// 设置超时
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 获取远程连接
	remoteConn, err := f.connPool.Get(ctx)
	if err != nil {
		f.logger.Printf("连接到远程失败: %v", err)
		conn.Close()
		return
	}

	// 使用零拷贝模式或标准模式
	if f.ZeroCopyEnabled {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			f.logger.Printf("使用零拷贝模式: %s <-> %s", clientAddr, f.Endpoint.Remote)
			err = zerocopy.TCPSplice(tcpConn, remoteConn, f.idleTimeout)
			if err != nil {
				f.handleConnectionError(err, clientAddr, true)
			}
			// 零拷贝模式下连接已关闭，不需要放回池中
			remoteConn.Close()
			return
		}
	}

	// 标准模式
	f.logger.Printf("使用标准模式: %s <-> %s", clientAddr, f.Endpoint.Remote)

	// 创建双向转发
	var wg sync.WaitGroup
	wg.Add(2)

	// 客户端 -> 远程
	go func() {
		defer wg.Done()
		err := f.forwardStream(ctx, conn, remoteConn, true, clientAddr)
		if err != nil {
			f.handleConnectionError(err, clientAddr, true)
		}
		conn.Close()
	}()

	// 远程 -> 客户端
	go func() {
		defer wg.Done()
		err := f.forwardStream(ctx, remoteConn, conn, false, clientAddr)
		if err != nil {
			f.handleConnectionError(err, clientAddr, false)
		}
		remoteConn.Close()
	}()

	// 等待两个方向都完成
	wg.Wait()

	// 尝试将连接放回池中
	if utils.IsConnectionError(err) {
		remoteConn.Close()
	} else {
		f.connPool.Put(remoteConn)
	}
}

// forwardStream 在两个连接之间转发数据流
func (f *Forwarder) forwardStream(ctx context.Context, src, dst net.Conn, isInbound bool, clientAddr string) error {
	// 创建缓冲区
	buffer := make([]byte, 32*1024)

	for {
		// 设置读取超时
		if err := src.SetReadDeadline(time.Now().Add(f.readTimeout)); err != nil {
			return err
		}

		// 从源读取
		n, err := src.Read(buffer)
		if err != nil {
			return err
		}

		// 如果启用了速率限制
		if f.limiter != nil && isInbound {
			// 计算需要等待的时间
			waitTime := f.limiter.ReserveN(time.Now(), n).Delay()
			if waitTime > 0 {
				time.Sleep(waitTime)
			}
		}

		// 设置写入超时
		if err := dst.SetWriteDeadline(time.Now().Add(f.writeTimeout)); err != nil {
			return err
		}

		// 写入目标
		_, err = dst.Write(buffer[:n])
		if err != nil {
			return err
		}

		// 更新统计
		f.Stats.UpdateStats(int64(n), isInbound)
	}
}

// handleConnectionError 处理连接错误
func (f *Forwarder) handleConnectionError(err error, remoteAddr string, isClient bool) {
	if utils.IsConnectionError(err) {
		direction := "客户端->远程"
		if !isClient {
			direction = "远程->客户端"
		}
		f.logger.Printf("连接关闭 (%s): %s, 原因: %v", direction, remoteAddr, err)
	} else {
		f.logger.Printf("转发错误: %v", err)
	}
}

// LogStats 记录统计信息
func (f *Forwarder) LogStats() {
	if !f.Endpoint.StatsEnabled {
		return
	}

	stats := f.Stats.GetFormattedStats(f.Endpoint.Local, f.Endpoint.Remote, f.ZeroCopyEnabled)
	f.logger.Printf("%s", stats)
}

// startUDP 启动UDP转发
func (f *Forwarder) startUDP() error {
	// UDP转发实现
	// 注意：此处仅提供基本实现框架，完整UDP转发需要更复杂的处理
	addr, err := net.ResolveUDPAddr("udp", f.Endpoint.Local)
	if err != nil {
		return fmt.Errorf("解析UDP地址失败: %v", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("监听UDP端口失败: %v", err)
	}

	f.logger.Printf("UDP转发已启动: %s -> %s", f.Endpoint.Local, f.Endpoint.Remote)

	go func() {
		buffer := make([]byte, 65507) // 最大UDP包大小
		for {
			n, clientAddr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				f.logger.Printf("读取UDP数据失败: %v", err)
				continue
			}

			go func(data []byte, size int, clientAddr *net.UDPAddr) {
				// 解析远程地址
				remoteAddr, err := net.ResolveUDPAddr("udp", f.Endpoint.Remote)
				if err != nil {
					f.logger.Printf("解析远程UDP地址失败: %v", err)
					return
				}

				// 连接到远程
				remoteConn, err := net.DialUDP("udp", nil, remoteAddr)
				if err != nil {
					f.logger.Printf("连接到远程UDP失败: %v", err)
					return
				}
				defer remoteConn.Close()

				// 发送数据到远程
				_, err = remoteConn.Write(data[:size])
				if err != nil {
					f.logger.Printf("发送UDP数据到远程失败: %v", err)
					return
				}

				// 更新统计
				f.Stats.UpdateStats(int64(size), true)

				// 接收远程响应
				remoteConn.SetReadDeadline(time.Now().Add(30 * time.Second))
				respSize, err := remoteConn.Read(buffer)
				if err != nil {
					f.logger.Printf("接收远程UDP响应失败: %v", err)
					return
				}

				// 发送响应回客户端
				_, err = conn.WriteToUDP(buffer[:respSize], clientAddr)
				if err != nil {
					f.logger.Printf("发送UDP响应到客户端失败: %v", err)
					return
				}

				// 更新统计
				f.Stats.UpdateStats(int64(respSize), false)
			}(append([]byte{}, buffer[:n]...), n, clientAddr)
		}
	}()

	return nil
}
