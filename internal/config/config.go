package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"

	lru "github.com/hashicorp/golang-lru"
)

// LogConfig 定义日志设置
type LogConfig struct {
	Level  string `json:"level"`  // Log level (e.g., "warn")
	Output string `json:"output"` // Log output file (e.g., "realm.log")
}

// Config 表示转发配置
type Config struct {
	Log       LogConfig     `json:"log"`
	Network   NetworkConfig `json:"network"`
	DNS       DNSConfig     `json:"dns"`
	Endpoints []Endpoint    `json:"endpoints"`
}

// NetworkConfig 定义网络设置
type NetworkConfig struct {
	NoTCP        bool `json:"no_tcp"`        // 禁用TCP
	UseUDP       bool `json:"use_udp"`       // 启用UDP
	ZeroCopy     bool `json:"zero_copy"`     // 启用零拷贝
	FastOpen     bool `json:"fast_open"`     // 启用TCP Fast Open
	UDPTimeout   int  `json:"udp_timeout"`   // UDP超时时间（秒）
	SendProxy    bool `json:"send_proxy"`    // 发送PROXY协议
	AcceptProxy  bool `json:"accept_proxy"`  // 接受PROXY协议
	ProxyVersion int  `json:"proxy_version"` // PROXY协议版本
	ProxyTimeout int  `json:"proxy_timeout"` // PROXY协议超时（秒）
}

// DNSConfig 定义DNS设置
type DNSConfig struct {
	Mode        string   `json:"mode"`        // DNS模式（如 "ipv4_only", "ipv6_only", "ipv4_and_ipv6"）
	Protocol    string   `json:"protocol"`    // DNS协议（如 "tcp_and_udp"）
	Nameservers []string `json:"nameservers"` // DNS服务器（如 ["8.8.8.8:53", "[2001:4860:4860::8888]:53"]）
	MinTTL      int      `json:"min_ttl"`     // 最小TTL（秒）
	MaxTTL      int      `json:"max_ttl"`     // 最大TTL（秒）
	CacheSize   int      `json:"cache_size"`  // DNS缓存大小
}

// Endpoint 定义单个转发规则
type Endpoint struct {
	Local        string `json:"local"`         // 监听地址（如 "[::]:8080"）
	Remote       string `json:"remote"`        // 目标地址（如 "example.com:80"）
	RateLimit    string `json:"rate_limit"`    // 速率限制（如 "10MB/s", "1GB/s"）
	RateLimitBps int64  `json:"-"`             // 计算后的速率限制（字节/秒）
	StatsEnabled bool   `json:"stats_enabled"` // 启用流量统计
}

// Load 加载并解析配置文件
func Load(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件失败: %v", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("解析配置文件失败: %v", err)
	}

	// 解析速率限制
	for i := range config.Endpoints {
		if config.Endpoints[i].RateLimit != "" {
			rateBps, err := ParseRateLimit(config.Endpoints[i].RateLimit)
			if err != nil {
				return Config{}, fmt.Errorf("端点 %s 的速率限制无效: %v",
					config.Endpoints[i].Local, err)
			}
			config.Endpoints[i].RateLimitBps = rateBps
		}
	}

	return config, nil
}

// SetupDNS 初始化DNS缓存和解析器
func SetupDNS(dnsConfig DNSConfig) (*lru.Cache, *net.Resolver) {
	// 初始化DNS缓存
	cache, err := lru.New(dnsConfig.CacheSize)
	if err != nil {
		panic(fmt.Sprintf("初始化DNS缓存失败: %v", err))
	}

	// 配置DNS解析器
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			protocol := dnsConfig.Protocol
			if protocol == "tcp_and_udp" {
				protocol = "udp" // 默认使用UDP，必要时回退到TCP
			}
			for _, ns := range dnsConfig.Nameservers {
				conn, err := d.DialContext(ctx, protocol, ns)
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("无法连接到任何DNS服务器")
		},
	}

	return cache, resolver
}
