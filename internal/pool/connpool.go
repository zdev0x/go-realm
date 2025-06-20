package pool

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zdev0x/go-realm/internal/config"
	"github.com/zdev0x/go-realm/pkg/utils"
)

// ConnPool 表示一个简单的连接池
type ConnPool struct {
	remote    string            // 目标地址
	conns     chan *net.TCPConn // 连接池通道
	size      int               // 池大小
	dialer    *net.Dialer       // 连接创建器
	mu        sync.RWMutex      // 保护连接池操作
	active    int32             // 当前活跃连接数
	resolver  *net.Resolver     // DNS解析器
	dnsConfig config.DNSConfig  // DNS配置
}

// NewConnPool 创建一个新的连接池
func NewConnPool(remote string, size int, resolver *net.Resolver, dnsConfig config.DNSConfig) *ConnPool {
	return &ConnPool{
		remote: remote,
		conns:  make(chan *net.TCPConn, size),
		size:   size,
		dialer: &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		},
		resolver:  resolver,
		dnsConfig: dnsConfig,
	}
}

// Get 从池中获取一个连接
func (p *ConnPool) Get(ctx context.Context) (*net.TCPConn, error) {
	// 尝试从池中获取连接
	select {
	case conn := <-p.conns:
		if conn == nil {
			return nil, fmt.Errorf("连接池已关闭")
		}
		// 验证连接是否可用
		if err := utils.ValidateConnection(conn); err != nil {
			conn.Close()
			atomic.AddInt32(&p.active, -1)
			// 创建新连接替代无效连接
			return p.createConn(ctx)
		}
		return conn, nil
	default:
		// 池为空，创建新连接
		return p.createConn(ctx)
	}
}

// Put 将连接放回池中
func (p *ConnPool) Put(conn *net.TCPConn) {
	if conn == nil {
		return
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	// 验证连接是否仍然有效
	if err := utils.ValidateConnection(conn); err != nil {
		conn.Close()
		atomic.AddInt32(&p.active, -1)
		return
	}

	// 尝试放回池中
	select {
	case p.conns <- conn:
		// 成功放回池中
	default:
		// 池已满，关闭连接
		conn.Close()
		atomic.AddInt32(&p.active, -1)
	}
}

// createConn 创建新的连接
func (p *ConnPool) createConn(ctx context.Context) (*net.TCPConn, error) {
	// 解析远程地址
	host, port, err := net.SplitHostPort(p.remote)
	if err != nil {
		return nil, err
	}

	// 如果是IP地址，直接使用
	if net.ParseIP(host) != nil {
		conn, err := p.dialer.DialContext(ctx, "tcp", p.remote)
		if err != nil {
			return nil, err
		}
		tcpConn := conn.(*net.TCPConn)
		if err := optimizeTCPConn(tcpConn); err != nil {
			tcpConn.Close()
			return nil, err
		}
		atomic.AddInt32(&p.active, 1)
		return tcpConn, nil
	}

	// DNS解析
	ips, err := p.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}

	// 根据DNS模式选择IP
	var ip string
	for _, ipAddr := range ips {
		switch p.dnsConfig.Mode {
		case "ipv4_only":
			if ipAddr.IP.To4() != nil {
				ip = ipAddr.IP.String()
				break
			}
		case "ipv6_only":
			if ipAddr.IP.To4() == nil && ipAddr.IP.To16() != nil {
				ip = ipAddr.IP.String()
				break
			}
		case "ipv4_and_ipv6":
			if ipAddr.IP.To16() != nil {
				ip = ipAddr.IP.String()
				break
			}
			if ipAddr.IP.To4() != nil && ip == "" { // Fallback to IPv4
				ip = ipAddr.IP.String()
			}
		}
	}

	if ip == "" {
		return nil, fmt.Errorf("no suitable IP found for %s", host)
	}

	// 格式化地址
	var addr string
	if net.ParseIP(ip).To4() == nil {
		addr = fmt.Sprintf("[%s]:%s", ip, port)
	} else {
		addr = fmt.Sprintf("%s:%s", ip, port)
	}

	// 建立连接
	conn, err := p.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	tcpConn := conn.(*net.TCPConn)
	if err := optimizeTCPConn(tcpConn); err != nil {
		tcpConn.Close()
		return nil, err
	}

	atomic.AddInt32(&p.active, 1)
	return tcpConn, nil
}

// Close 关闭连接池
func (p *ConnPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 关闭所有连接
	close(p.conns)
	for conn := range p.conns {
		if conn != nil {
			conn.Close()
			atomic.AddInt32(&p.active, -1)
		}
	}
}

// Stats 获取连接池统计信息
func (p *ConnPool) Stats() (active int32, pooled int) {
	return atomic.LoadInt32(&p.active), len(p.conns)
}

// optimizeTCPConn 优化TCP连接参数
func optimizeTCPConn(conn *net.TCPConn) error {
	// 设置TCP选项
	if err := conn.SetNoDelay(true); err != nil {
		return err
	}
	if err := conn.SetKeepAlive(true); err != nil {
		return err
	}
	if err := conn.SetKeepAlivePeriod(30 * time.Second); err != nil {
		return err
	}
	return nil
}
