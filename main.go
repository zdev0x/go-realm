package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"golang.org/x/time/rate"
)

// Config represents the forwarding configuration
type Config struct {
	Log       LogConfig     `json:"log"`
	Network   NetworkConfig `json:"network"`
	DNS       DNSConfig     `json:"dns"`
	Endpoints []Endpoint    `json:"endpoints"`
}

// LogConfig defines logging settings
type LogConfig struct {
	Level  string `json:"level"`  // Log level (e.g., "warn")
	Output string `json:"output"` // Log output file (e.g., "realm.log")
}

// NetworkConfig defines network settings
type NetworkConfig struct {
	NoTCP  bool `json:"no_tcp"`  // Disable TCP
	UseUDP bool `json:"use_udp"` // Enable UDP
}

// DNSConfig defines DNS settings
type DNSConfig struct {
	Mode        string   `json:"mode"`        // DNS mode (e.g., "ipv4_only", "ipv6_only", "ipv4_and_ipv6")
	Protocol    string   `json:"protocol"`    // DNS protocol (e.g., "tcp_and_udp")
	Nameservers []string `json:"nameservers"` // DNS servers (e.g., ["8.8.8.8:53", "[2001:4860:4860::8888]:53"])
	MinTTL      int      `json:"min_ttl"`     // Minimum TTL in seconds
	MaxTTL      int      `json:"max_ttl"`     // Maximum TTL in seconds
	CacheSize   int      `json:"cache_size"`  // DNS cache size
}

// Endpoint defines a single forwarding rule
type Endpoint struct {
	Local        string `json:"local"`         // Listening address (e.g., "[::]:8080")
	Remote       string `json:"remote"`        // Target address (e.g., "example.com:80")
	RateLimit    string `json:"rate_limit"`    // Rate limit (e.g., "10MB/s", "1GB/s")
	RateLimitBps int64  `json:"-"`             // Calculated rate limit in bytes per second
	StatsEnabled bool   `json:"stats_enabled"` // Enable traffic statistics
}

// Stats tracks traffic statistics for an endpoint
type Stats struct {
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
	LastUpdated time.Time `json:"last_updated"`
	// 新增字段用于计算速率
	BytesInPerSecond  float64   `json:"bytes_in_per_second"`
	BytesOutPerSecond float64   `json:"bytes_out_per_second"`
	LastBytesIn       int64     `json:"-"`
	LastBytesOut      int64     `json:"-"`
	LastCalcTime      time.Time `json:"-"`
	// 新增累计统计
	TotalConnections  int64 `json:"total_connections"`
	ActiveConnections int32 `json:"active_connections"`
}

// ConnPool 表示一个简单的连接池
type ConnPool struct {
	remote    string            // 目标地址
	conns     chan *net.TCPConn // 连接池通道
	size      int               // 池大小
	dialer    *net.Dialer       // 连接创建器
	mu        sync.RWMutex      // 保护连接池操作
	active    int32             // 当前活跃连接数
	resolver  *net.Resolver     // DNS解析器
	dnsConfig DNSConfig         // DNS配置
}

// NewConnPool 创建一个新的连接池
func NewConnPool(remote string, size int, resolver *net.Resolver, dnsConfig DNSConfig) *ConnPool {
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
		if err := validateConnection(conn); err != nil {
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
	if err := validateConnection(conn); err != nil {
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

// Stats 返回连接池统计信息
func (p *ConnPool) Stats() (active int32, pooled int) {
	return atomic.LoadInt32(&p.active), len(p.conns)
}

// Forwarder manages a single forwarding rule
type Forwarder struct {
	Endpoint        Endpoint
	Stats           *Stats
	StatsLock       sync.Mutex
	DNSCache        *lru.Cache
	Resolver        *net.Resolver
	DNSConfig       DNSConfig
	readTimeout     time.Duration
	writeTimeout    time.Duration
	idleTimeout     time.Duration
	connPool        *ConnPool // 新增连接池
	ZeroCopyEnabled bool      // 是否启用零拷贝
	limiter         *rate.Limiter
}

// min returns the smaller of x or y
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// parseRateLimit parses rate limit string like "10MB/s" to bytes per second
func parseRateLimit(rateStr string) (int64, error) {
	if rateStr == "" {
		return 0, nil
	}

	// 移除空格和所有可能的后缀
	rateStr = strings.TrimSpace(rateStr)
	rateStr = strings.ToUpper(rateStr)
	rateStr = strings.TrimSuffix(rateStr, "/S")
	rateStr = strings.TrimSuffix(rateStr, "/SEC")
	rateStr = strings.TrimSuffix(rateStr, "PS")
	rateStr = strings.TrimSuffix(rateStr, "BPS")

	// 支持的单位（按从大到小排序，避免部分匹配问题）
	units := map[string]int64{
		"TB": 1024 * 1024 * 1024 * 1024,
		"GB": 1024 * 1024 * 1024,
		"MB": 1024 * 1024,
		"KB": 1024,
		"B":  1,
	}

	// 查找单位和数值
	var valueStr string
	var multiplier int64

	// 尝试匹配单位
	found := false
	for u, m := range units {
		if strings.HasSuffix(rateStr, u) {
			multiplier = m
			valueStr = strings.TrimSuffix(rateStr, u)
			found = true
			break
		}
	}

	// 如果没有找到单位，假设是纯数字（字节每秒）
	if !found {
		valueStr = rateStr
		multiplier = 1
	}

	// 解析数值部分
	value, err := strconv.ParseFloat(strings.TrimSpace(valueStr), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate limit format: %s (failed to parse number)", rateStr)
	}

	if value < 0 {
		return 0, fmt.Errorf("rate limit cannot be negative: %s", rateStr)
	}

	// 计算最终的字节每秒值
	bps := int64(value * float64(multiplier))

	// 验证结果是否合理
	if bps < 0 {
		return 0, fmt.Errorf("rate limit overflow: %s", rateStr)
	}

	return bps, nil
}

var logChan chan string
var logWg sync.WaitGroup

func logAsyncPrintf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	select {
	case logChan <- msg:
		// ok
	default:
		// 日志通道满时丢弃，防止阻塞主流程
	}
}

func startAsyncLogger() {
	logChan = make(chan string, 10000)
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		for msg := range logChan {
			log.Print(msg)
		}
	}()
}

func stopAsyncLogger() {
	close(logChan)
	logWg.Wait()
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	startAsyncLogger()
	defer stopAsyncLogger()

	// 显示启动标志
	fmt.Println(`
╔═══════════════════════════════════════════╗
║             Go-Realm Starting             ║
║      High Performance Port Forwarder      ║
╚═══════════════════════════════════════════╝
`)

	// Load configuration
	config, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 解析并验证配置
	fmt.Println("📝 Loading configuration...")

	// 验证并解析每个端点的限速设置
	for i, endpoint := range config.Endpoints {
		if endpoint.RateLimit != "" {
			rateBps, err := parseRateLimit(endpoint.RateLimit)
			if err != nil {
				log.Fatalf("Invalid rate limit for endpoint %s: %v", endpoint.Local, err)
			}
			config.Endpoints[i].RateLimitBps = rateBps
		}
	}

	// Configure logging
	fmt.Println("📋 Configuring logging...")
	configureLogging(config.Log)

	// Initialize DNS cache
	fmt.Println("🔄 Initializing DNS cache...")
	cache, err := lru.New(config.DNS.CacheSize)
	if err != nil {
		log.Fatalf("Failed to initialize DNS cache: %v", err)
	}

	// Configure DNS resolver
	fmt.Println("🌐 Configuring DNS resolver...")
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			protocol := config.DNS.Protocol
			if protocol == "tcp_and_udp" {
				protocol = "udp" // Default to UDP for DNS, fallback to TCP if needed
			}
			for _, ns := range config.DNS.Nameservers {
				conn, err := d.DialContext(ctx, protocol, ns)
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("failed to connect to any nameserver")
		},
	}

	// 创建forwarders map用于跟踪所有forwarder实例
	forwarders := make(map[string]*Forwarder)
	var forwardersLock sync.RWMutex

	// Start forwarders for each endpoint
	fmt.Println("\n🚀 Starting forwarders...")
	var wg sync.WaitGroup
	for _, endpoint := range config.Endpoints {
		f := NewForwarder(endpoint, cache, resolver, config.DNS)

		// 显示端点信息
		fmt.Printf("\n📌 Endpoint: %s -> %s\n", endpoint.Local, endpoint.Remote)
		if endpoint.RateLimitBps > 0 {
			fmt.Printf("   Rate Limit: %s (%d bytes/s)\n", endpoint.RateLimit, endpoint.RateLimitBps)
		} else {
			fmt.Printf("   Rate Limit: unlimited\n")
		}
		fmt.Printf("   Stats Enabled: %v\n", endpoint.StatsEnabled)

		// 将forwarder添加到map中
		forwardersLock.Lock()
		forwarders[endpoint.Local] = f
		forwardersLock.Unlock()

		wg.Add(1)
		go func(f *Forwarder) {
			defer wg.Done()
			if err := f.start(config.Network); err != nil {
				logAsyncPrintf("Error starting forwarder for %s: %v", f.Endpoint.Local, err)
			}
		}(f)
	}

	fmt.Println("\n✅ All forwarders started successfully!")
	fmt.Println("📊 Traffic statistics will be logged every 1 second...")

	// 定期统计信息输出
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			forwardersLock.RLock()
			for _, f := range forwarders {
				if f.Endpoint.StatsEnabled {
					f.logStats()
				}
			}
			forwardersLock.RUnlock()
		}
	}()

	// 优雅关闭处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("\n\n🛑 Received signal %v, shutting down...\n", sig)

		// 输出最终统计信息
		fmt.Println("\n📊 Final Statistics:")
		forwardersLock.RLock()
		for _, f := range forwarders {
			if f.Endpoint.StatsEnabled {
				f.logStats()
			}
		}
		forwardersLock.RUnlock()

		fmt.Println("\n👋 Goodbye!")
		os.Exit(0)
	}()

	wg.Wait()
}

// configureLogging sets up logging based on config
func configureLogging(logConfig LogConfig) {
	if logConfig.Output != "" {
		f, err := os.OpenFile(logConfig.Output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("Failed to open log file %s: %v", logConfig.Output, err)
		}
		log.SetOutput(f)
	}
	if logConfig.Level == "warn" {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}
}

// loadConfig reads and parses the configuration file
func loadConfig(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// resolveRemote resolves the remote address, using cache if available
func (f *Forwarder) resolveRemote() (string, error) {
	host, port, err := net.SplitHostPort(f.Endpoint.Remote)
	if err != nil {
		return "", err
	}

	// Check if host is an IP address (IPv4 or IPv6)
	if net.ParseIP(host) != nil {
		return f.Endpoint.Remote, nil
	}

	// Check cache
	cacheKey := fmt.Sprintf("%s:%s", host, f.DNSConfig.Mode)
	if cached, ok := f.DNSCache.Get(cacheKey); ok {
		return fmt.Sprintf("%s:%s", cached, port), nil
	}

	// Perform DNS lookup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := f.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("DNS lookup failed for %s: %v", host, err)
	}

	var ip string
	for _, ipAddr := range ips {
		switch f.DNSConfig.Mode {
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
			if ipAddr.IP.To16() != nil { // Prefer IPv6
				ip = ipAddr.IP.String()
				break
			}
			if ipAddr.IP.To4() != nil && ip == "" { // Fallback to IPv4
				ip = ipAddr.IP.String()
			}
		}
	}
	if ip == "" {
		return "", fmt.Errorf("no suitable IP found for %s with mode %s", host, f.DNSConfig.Mode)
	}

	// Cache the result
	ttl := time.Duration(f.DNSConfig.MinTTL) * time.Second
	if ttl > time.Duration(f.DNSConfig.MaxTTL)*time.Second {
		ttl = time.Duration(f.DNSConfig.MaxTTL) * time.Second
	}
	f.DNSCache.Add(cacheKey, ip)

	// Format IPv6 address with brackets if needed
	if net.ParseIP(ip).To4() == nil && net.ParseIP(ip).To16() != nil {
		return fmt.Sprintf("[%s]:%s", ip, port), nil
	}
	return fmt.Sprintf("%s:%s", ip, port), nil
}

// start begins the forwarding process for an endpoint
func (f *Forwarder) start(network NetworkConfig) error {
	var wg sync.WaitGroup
	var tcpErr, udpErr error

	if !network.NoTCP {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tcpErr = f.startTCP()
		}()
	}

	if network.UseUDP {
		wg.Add(1)
		go func() {
			defer wg.Done()
			udpErr = f.startUDP()
		}()
	}

	wg.Wait()

	if tcpErr != nil && udpErr != nil && !network.NoTCP && network.UseUDP {
		return fmt.Errorf("both TCP and UDP failed: TCP=%v, UDP=%v", tcpErr, udpErr)
	}
	return nil
}

// startTCP handles TCP forwarding
func (f *Forwarder) startTCP() error {
	listener, err := net.Listen("tcp", f.Endpoint.Local)
	if err != nil {
		logAsyncPrintf("TCP listen error on %s: %v", f.Endpoint.Local, err)
		return err
	}
	defer listener.Close()
	logAsyncPrintf("Listening on %s (TCP) forwarding to %s", f.Endpoint.Local, f.Endpoint.Remote)

	for {
		conn, err := listener.Accept()
		if err != nil {
			logAsyncPrintf("TCP accept error on %s: %v", f.Endpoint.Local, err)
			continue
		}
		go f.handleTCPConn(conn)
	}
}

// optimizeTCPConn optimizes TCP connection parameters
func optimizeTCPConn(conn *net.TCPConn) error {
	if err := conn.SetKeepAlive(true); err != nil {
		return fmt.Errorf("set keep-alive: %v", err)
	}
	if err := conn.SetKeepAlivePeriod(30 * time.Second); err != nil {
		return fmt.Errorf("set keep-alive period: %v", err)
	}
	if err := conn.SetNoDelay(true); err != nil {
		return fmt.Errorf("set TCP_NODELAY: %v", err)
	}

	// 设置更大的缓冲区
	if err := conn.SetReadBuffer(1024 * 1024); err != nil {
		return fmt.Errorf("set read buffer: %v", err)
	}
	if err := conn.SetWriteBuffer(1024 * 1024); err != nil {
		return fmt.Errorf("set write buffer: %v", err)
	}

	return nil
}

// handleTCPConn processes a single TCP connection
func (f *Forwarder) handleTCPConn(conn net.Conn) {
	defer conn.Close()

	if f.Endpoint.StatsEnabled {
		f.StatsLock.Lock()
		f.Stats.TotalConnections++
		atomic.AddInt32(&f.Stats.ActiveConnections, 1)
		f.StatsLock.Unlock()
		defer atomic.AddInt32(&f.Stats.ActiveConnections, -1)
	}

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := optimizeTCPConn(tcpConn); err != nil {
			logAsyncPrintf("优化客户端连接失败: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	target, err := f.connPool.Get(ctx)
	if err != nil {
		logAsyncPrintf("无法从连接池获取连接 %s: %v", f.Endpoint.Remote, err)
		return
	}
	defer f.connPool.Put(target)

	// 零拷贝分支
	if f.ZeroCopyEnabled {
		clientTCP, ok1 := conn.(*net.TCPConn)
		if ok1 {
			logAsyncPrintf("[ZeroCopy] 启用splice零拷贝转发，统计/限速不生效: %s -> %s", f.Endpoint.Local, f.Endpoint.Remote)
			if err := zeroCopyTCPSplice(clientTCP, target, f.idleTimeout); err == nil {
				return
			} else {
				logAsyncPrintf("[ZeroCopy] splice失败，降级为普通转发: %v", err)
			}
		}
	}

	// 创建用于转发的上下文
	forwardCtx, forwardCancel := context.WithTimeout(context.Background(), f.idleTimeout)
	defer forwardCancel()

	// 使用 WaitGroup 和 channel 确保两个 goroutine 都结束后再关闭连接
	var wg sync.WaitGroup
	errChan := make(chan error, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		errChan <- f.forwardStream(forwardCtx, conn, target, true, conn.RemoteAddr().String())
	}()

	go func() {
		defer wg.Done()
		errChan <- f.forwardStream(forwardCtx, target, conn, false, conn.RemoteAddr().String())
	}()

	// 等待两个转发goroutine都完成后关闭channel
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// 处理两个goroutine返回的错误
	for err := range errChan {
		if err != nil && !isConnectionError(err) {
			// 只记录非预期的连接错误
			logAsyncPrintf("转发错误 %s: %v", conn.RemoteAddr().String(), err)
		}
	}
}

// forwardStream handles one direction of data transfer
func (f *Forwarder) forwardStream(ctx context.Context, src, dst net.Conn, isInbound bool, clientAddr string) error {
	buffer := make([]byte, 128*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			deadline := time.Now().Add(f.readTimeout)
			if err := src.SetReadDeadline(deadline); err != nil {
				return fmt.Errorf("设置读取期限失败: %v", err)
			}

			n, err := src.Read(buffer)
			if n > 0 {
				// 限速逻辑：每次读到数据后写出前限速
				if f.limiter != nil {
					if err := f.limiter.WaitN(ctx, n); err != nil {
						return err
					}
				}

				if f.Endpoint.StatsEnabled {
					f.updateStats(int64(n), isInbound)
				}

				deadline = time.Now().Add(f.writeTimeout)
				if err := dst.SetWriteDeadline(deadline); err != nil {
					return fmt.Errorf("设置写入期限失败: %v", err)
				}

				_, writeErr := dst.Write(buffer[:n])
				if writeErr != nil {
					f.handleConnectionError(writeErr, dst.RemoteAddr().String(), false)
					return writeErr
				}
			}

			if err != nil {
				if err == io.EOF {
					return nil
				}
				f.handleConnectionError(err, src.RemoteAddr().String(), isInbound)
				return err
			}
		}
	}
}

// isConnectionError 检查是否为连接相关错误
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// 检查具体错误类型
	if err == io.EOF {
		return true
	}

	// 检查网络错误
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || netErr.Temporary()
	}

	// 检查操作错误
	if opErr, ok := err.(*net.OpError); ok {
		// 检查具体错误信息
		errStr := opErr.Err.Error()
		return strings.Contains(errStr, "connection reset by peer") ||
			strings.Contains(errStr, "broken pipe") ||
			strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "no route to host") ||
			strings.Contains(errStr, "network is unreachable")
	}

	return false
}

// handleConnectionError 处理连接错误
func (f *Forwarder) handleConnectionError(err error, remoteAddr string, isClient bool) {
	if err == nil {
		return
	}

	// 只记录非预期的错误
	if !isConnectionError(err) {
		if isClient {
			logAsyncPrintf("客户端连接异常 %s: %v", remoteAddr, err)
		} else {
			logAsyncPrintf("目标服务器连接异常 %s: %v", remoteAddr, err)
		}
	} else {
		// 对于预期的连接错误，使用调试级别的日志
		if isClient {
			logAsyncPrintf("客户端 %s 断开连接: %v", remoteAddr, err)
		} else {
			logAsyncPrintf("与目标服务器 %s 的连接断开: %v", remoteAddr, err)
		}
	}
}

// validateConnection 验证连接是否有效
func validateConnection(conn *net.TCPConn) error {
	if conn == nil {
		return fmt.Errorf("空连接")
	}

	// 设置一个极短的写入超时，用于探测连接是否可写
	// 零字节写入是一个轻量级的探测方式，不会实际发送数据
	// 但如果连接已断开（例如 RST），它会立即返回错误
	deadline := time.Now().Add(time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("设置写入期限失败: %v", err)
	}

	// 尝试写入0字节数据来探测连接状态
	if _, err := conn.Write([]byte{}); err != nil {
		return fmt.Errorf("连接已断开: %v", err)
	}

	// 重置写入期限
	return conn.SetWriteDeadline(time.Time{})
}

// updateStats safely updates traffic statistics
func (f *Forwarder) updateStats(bytesTransferred int64, isInbound bool) {
	if !f.Endpoint.StatsEnabled {
		return
	}

	f.StatsLock.Lock()
	defer f.StatsLock.Unlock()

	now := time.Now()

	// 更新总字节数
	if isInbound {
		f.Stats.BytesIn += bytesTransferred
	} else {
		f.Stats.BytesOut += bytesTransferred
	}

	// 计算速率（每秒）
	if !f.Stats.LastCalcTime.IsZero() {
		duration := now.Sub(f.Stats.LastCalcTime).Seconds()
		if duration >= 1.0 { // 至少1秒才更新速率
			if isInbound {
				bytesPerSecond := float64(f.Stats.BytesIn-f.Stats.LastBytesIn) / duration
				f.Stats.BytesInPerSecond = bytesPerSecond
				f.Stats.LastBytesIn = f.Stats.BytesIn
			} else {
				bytesPerSecond := float64(f.Stats.BytesOut-f.Stats.LastBytesOut) / duration
				f.Stats.BytesOutPerSecond = bytesPerSecond
				f.Stats.LastBytesOut = f.Stats.BytesOut
			}
			f.Stats.LastCalcTime = now
		}
	} else {
		// 首次更新
		f.Stats.LastCalcTime = now
		if isInbound {
			f.Stats.LastBytesIn = f.Stats.BytesIn
		} else {
			f.Stats.LastBytesOut = f.Stats.BytesOut
		}
	}

	f.Stats.LastUpdated = now
}

// logStats prints traffic statistics
func (f *Forwarder) logStats() {
	if !f.Endpoint.StatsEnabled {
		return
	}

	f.StatsLock.Lock()
	stats := *f.Stats // 复制统计数据以减少锁定时间
	f.StatsLock.Unlock()

	// 格式化速率显示
	inRate := formatBytesPerSecond(stats.BytesInPerSecond)
	outRate := formatBytesPerSecond(stats.BytesOutPerSecond)
	totalIn := formatBytes(stats.BytesIn)
	totalOut := formatBytes(stats.BytesOut)

	if f.ZeroCopyEnabled {
		logAsyncPrintf("[ZeroCopy] Stats for %s -> %s:\n  Total: %s in, %s out\n  Rate: %s/s in, %s/s out\n  Connections: %d total, %d active\n  Last updated: %s\n  [警告] 零拷贝模式下统计仅供参考，可能不准确", f.Endpoint.Local, f.Endpoint.Remote, totalIn, totalOut, inRate, outRate, stats.TotalConnections, atomic.LoadInt32(&stats.ActiveConnections), stats.LastUpdated.Format("2006-01-02 15:04:05"))
		return
	}

	logAsyncPrintf("Stats for %s -> %s:\n  Total: %s in, %s out\n  Rate: %s/s in, %s/s out\n  Connections: %d total, %d active\n  Last updated: %s", f.Endpoint.Local, f.Endpoint.Remote, totalIn, totalOut, inRate, outRate, stats.TotalConnections, atomic.LoadInt32(&stats.ActiveConnections), stats.LastUpdated.Format("2006-01-02 15:04:05"))
}

// formatBytes converts bytes to human readable format
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatBytesPerSecond converts bytes per second to human readable format
func formatBytesPerSecond(bytesPerSecond float64) string {
	const unit = 1024.0
	if bytesPerSecond < unit {
		return fmt.Sprintf("%.2f B", bytesPerSecond)
	}
	div, exp := float64(unit), 0
	for n := bytesPerSecond / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", bytesPerSecond/div, "KMGTPE"[exp])
}

func NewForwarder(endpoint Endpoint, cache *lru.Cache, resolver *net.Resolver, dnsConfig DNSConfig) *Forwarder {
	f := &Forwarder{
		Endpoint:     endpoint,
		Stats:        &Stats{LastUpdated: time.Now()},
		DNSCache:     cache,
		Resolver:     resolver,
		DNSConfig:    dnsConfig,
		readTimeout:  2 * time.Minute,
		writeTimeout: 30 * time.Second,
		idleTimeout:  5 * time.Minute,
	}

	// 创建连接池
	f.connPool = NewConnPool(endpoint.Remote, 200, resolver, dnsConfig) // 默认池大小为200

	// 检查zero_copy配置
	f.ZeroCopyEnabled = false
	if v, ok := any(dnsConfig).(map[string]interface{}); ok {
		if netcfg, ok := v["network"].(map[string]interface{}); ok {
			if zc, ok := netcfg["zero_copy"].(bool); ok && zc {
				f.ZeroCopyEnabled = true
			}
		}
	}

	// 在NewForwarder中初始化f.limiter = rate.NewLimiter(rate.Limit(endpoint.RateLimitBps), int(endpoint.RateLimitBps))
	f.limiter = rate.NewLimiter(rate.Limit(endpoint.RateLimitBps), int(endpoint.RateLimitBps))

	return f
}

// startUDP handles UDP forwarding
func (f *Forwarder) startUDP() error {
	addr, err := net.ResolveUDPAddr("udp", f.Endpoint.Local)
	if err != nil {
		logAsyncPrintf("UDP解析错误 %s: %v", f.Endpoint.Local, err)
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		logAsyncPrintf("UDP监听错误 %s: %v", f.Endpoint.Local, err)
		return err
	}
	defer conn.Close()
	logAsyncPrintf("监听 %s (UDP) 转发到 %s", f.Endpoint.Local, f.Endpoint.Remote)

	remoteAddr, err := f.resolveRemote()
	if err != nil {
		return err
	}
	targetAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		return err
	}

	// 创建用于转发的上下文
	forwardCtx, forwardCancel := context.WithTimeout(context.Background(), f.idleTimeout)
	defer forwardCancel()

	buffer := make([]byte, 65535)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			f.handleConnectionError(err, conn.LocalAddr().String(), true)
			continue
		}

		// Apply rate limiting with chunking
		if err := f.limiter.WaitN(forwardCtx, n); err != nil {
			logAsyncPrintf("限速错误 %s: %v", f.Endpoint.Local, err)
			continue
		}

		// Update stats
		if f.Endpoint.StatsEnabled {
			f.updateStats(int64(n), true)
		}

		// Forward to target
		targetConn, err := net.DialUDP("udp", nil, targetAddr)
		if err != nil {
			logAsyncPrintf("无法连接到 %s (UDP) 来自 %s: %v", remoteAddr, clientAddr.String(), err)
			continue
		}

		// Write with rate limiting
		_, err = targetConn.Write(buffer[:n])
		if err != nil {
			f.handleConnectionError(err, remoteAddr, false)
			targetConn.Close()
			continue
		}

		// Update stats for outbound
		if f.Endpoint.StatsEnabled {
			f.updateStats(int64(n), false)
		}

		// Handle response (if any)
		go func() {
			defer targetConn.Close()
			n, err := targetConn.Read(buffer)
			if err != nil {
				f.handleConnectionError(err, remoteAddr, false)
				return
			}

			// Apply rate limiting for response
			if err := f.limiter.WaitN(forwardCtx, n); err != nil {
				logAsyncPrintf("限速错误 %s: %v", f.Endpoint.Local, err)
				return
			}

			// Update stats
			if f.Endpoint.StatsEnabled {
				f.updateStats(int64(n), false)
			}

			// Send response back to client with rate limiting
			_, err = conn.WriteToUDP(buffer[:n], clientAddr)
			if err != nil {
				f.handleConnectionError(err, clientAddr.String(), true)
			}
		}()
	}
}

// splice相关常量
const (
	SPLICE_F_MOVE     = 0x01
	SPLICE_F_NONBLOCK = 0x02
	SPLICE_F_MORE     = 0x04
)

// 零拷贝splice实现
func zeroCopyTCPSplice(src, dst *net.TCPConn, idleTimeout time.Duration) error {
	srcFile, err := src.File()
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := dst.File()
	if err != nil {
		return err
	}
	defer dstFile.Close()
	fds := make([]int, 2)
	if err := syscall.Pipe(fds); err != nil {
		return err
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])
	// 双向转发
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			// src->dst
			n, err := syscall.Splice(int(srcFile.Fd()), nil, fds[1], nil, 128*1024, SPLICE_F_MOVE|SPLICE_F_MORE)
			if n == 0 || err != nil {
				return
			}
			_, err = syscall.Splice(fds[0], nil, int(dstFile.Fd()), nil, int(n), SPLICE_F_MOVE|SPLICE_F_MORE)
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			// dst->src
			n, err := syscall.Splice(int(dstFile.Fd()), nil, fds[1], nil, 128*1024, SPLICE_F_MOVE|SPLICE_F_MORE)
			if n == 0 || err != nil {
				return
			}
			_, err = syscall.Splice(fds[0], nil, int(srcFile.Fd()), nil, int(n), SPLICE_F_MOVE|SPLICE_F_MORE)
			if err != nil {
				return
			}
		}
	}()
	<-done
	<-done
	return nil
}
