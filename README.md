# Go-Realm

[![Go Version](https://img.shields.io/badge/Go-1.20%2B-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

高性能、多功能的网络端口转发工具，支持 TCP/UDP、IPv6、域名解析、限速和流量统计。

![Go-Realm Banner](https://via.placeholder.com/800x200?text=Go-Realm)

## 📋 简介

Go-Realm 是一个基于 Go 语言开发的网络流量转发工具，参考 [realm](https://github.com/zhboner/realm) 的配置格式和功能设计，提供高效、可靠的 TCP 和 UDP 流量转发服务。项目采用模块化设计，支持 IPv6 转发、域名解析、透明转发、限速以及流量统计功能，适用于多种网络场景。

### 核心优势

- **高性能**：采用 Go 语言并发模型，支持高并发连接
- **零拷贝**：Linux 系统下支持零拷贝技术，提升传输效率
- **全面兼容**：完全兼容 realm 的配置格式，便于迁移
- **IPv6 支持**：全面支持 IPv6 监听、转发和域名解析
- **模块化设计**：清晰的代码结构，易于维护和扩展

## ✨ 特性

- **TCP/UDP 转发**
  - 支持 TCP 和 UDP 协议的透明转发
  - 无需解析上层协议（如 SOCKS5、HTTP），直接转发原始数据流
  - 支持双向数据传输，适用于各种应用层协议

- **IPv6 全面支持**
  - 支持在 IPv6 地址上监听（如 `[::]:8080`）
  - 支持连接到 IPv6 目标地址（如 `[2001:db8::1]:80`）
  - 支持通过域名解析获得 IPv6 地址

- **智能域名解析**
  - 可配置 DNS 服务器（支持 IPv4 和 IPv6）
  - 多种解析模式：仅 IPv4、仅 IPv6、IPv4+IPv6
  - DNS 缓存机制，减少重复查询

- **精准限速控制**
  - 为每个转发端点单独配置限速
  - 支持人性化的速率表示（如 "10MB/s"）
  - 基于 `golang.org/x/time/rate` 实现精准流量控制

- **实时流量统计**
  - 记录每个端点的入站和出站流量
  - 监控连接数量和速率
  - 支持人性化的流量展示

- **零拷贝优化**
  - Linux 系统下支持零拷贝技术
  - 减少数据复制和内存使用
  - 提高大流量场景下的性能

- **高级连接池**
  - 连接复用，减少建立连接的开销
  - 动态缓冲区，根据流量自动调整大小
  - 提升高并发场景下的性能

## 🚀 安装

### 系统要求

- Go 1.20 或更高版本
- 支持 Linux、Windows、macOS（零拷贝功能仅支持 Linux）
- 支持 IPv4/IPv6 的网络环境

### 从源码安装

```bash
# 克隆仓库
git clone https://github.com/zdev0x/go-realm.git
cd go-realm

# 安装依赖
go mod download

# 编译
go build -o go-realm ./cmd/go-realm

# 安装到系统路径（可选）
sudo mv go-realm /usr/local/bin/
```

### 使用 Go Install（可选）

```bash
go install github.com/zdev0x/go-realm/cmd/go-realm@latest
```

## 🏁 快速开始

### 基本配置

创建一个简单的配置文件 `config.json`：

```json
{
  "log": {
    "level": "warn",
    "output": "go-realm.log"
  },
  "endpoints": [
    {
      "local": "0.0.0.0:8080",
      "remote": "example.com:80",
      "stats_enabled": true
    }
  ]
}
```

### 运行服务

```bash
./go-realm -config config.json
```

### 测试转发

```bash
# 测试 HTTP 转发
curl http://localhost:8080

# 如果配置了 SOCKS5 转发
curl --socks5 localhost:1080 http://example.com
```

## ⚙️ 配置详解

Go-Realm 使用 JSON 格式的配置文件，完全兼容 realm 的配置格式。

### 配置文件结构

配置文件包含四个主要部分：

```json
{
  "log": { ... },      // 日志配置
  "network": { ... },  // 网络配置
  "dns": { ... },      // DNS 配置
  "endpoints": [ ... ] // 转发端点配置
}
```

### 日志配置

```json
"log": {
  "level": "warn",           // 日志级别：warn, error
  "output": "go-realm.log"   // 日志输出文件，留空则输出到标准输出
}
```

### 网络配置

```json
"network": {
  "no_tcp": false,           // 是否禁用 TCP 转发
  "use_udp": true,           // 是否启用 UDP 转发
  "zero_copy": true,         // 是否启用零拷贝（Linux 系统）
  "fast_open": true,         // 是否启用 TCP Fast Open
  "udp_timeout": 30,         // UDP 会话超时时间（秒）
  "send_proxy": false,       // 是否发送 PROXY 协议头
  "send_proxy_version": 2,   // PROXY 协议版本
  "accept_proxy": false,     // 是否接受 PROXY 协议头
  "accept_proxy_timeout": 5  // 接受 PROXY 协议头的超时时间（秒）
}
```

### DNS 配置

```json
"dns": {
  "mode": "ipv4_and_ipv6",   // DNS 解析模式：ipv4_only, ipv6_only, ipv4_and_ipv6
  "protocol": "tcp_and_udp", // DNS 查询协议：tcp, udp, tcp_and_udp
  "nameservers": [           // DNS 服务器列表
    "8.8.8.8:53",
    "[2001:4860:4860::8888]:53"
  ],
  "min_ttl": 600,            // 最小 TTL（秒）
  "max_ttl": 3600,           // 最大 TTL（秒）
  "cache_size": 256          // DNS 缓存大小
}
```

### 端点配置

```json
"endpoints": [
  {
    "local": "0.0.0.0:8080",     // 本地监听地址
    "remote": "example.com:80",  // 远程目标地址
    "rate_limit": "10MB/s",      // 速率限制（支持 KB/s, MB/s, GB/s 等）
    "stats_enabled": true        // 是否启用流量统计
  }
]
```

### 完整配置示例

```json
{
  "log": {
    "level": "warn",
    "output": "go-realm.log"
  },
  "network": {
    "no_tcp": false,
    "use_udp": true,
    "zero_copy": true,
    "fast_open": true,
    "udp_timeout": 30,
    "send_proxy": false,
    "send_proxy_version": 2,
    "accept_proxy": false,
    "accept_proxy_timeout": 5
  },
  "dns": {
    "mode": "ipv4_and_ipv6",
    "protocol": "tcp_and_udp",
    "nameservers": [
      "8.8.8.8:53",
      "[2001:4860:4860::8888]:53"
    ],
    "min_ttl": 600,
    "max_ttl": 3600,
    "cache_size": 256
  },
  "endpoints": [
    {
      "local": "0.0.0.0:8080",
      "remote": "example.com:80",
      "rate_limit": "10MB/s",
      "stats_enabled": true
    },
    {
      "local": "[::]:1080",
      "remote": "socks-server.example.com:1080",
      "rate_limit": "5MB/s",
      "stats_enabled": true
    },
    {
      "local": "0.0.0.0:53",
      "remote": "8.8.8.8:53",
      "rate_limit": "1MB/s",
      "stats_enabled": false
    }
  ]
}
```

## 🔍 使用场景

### HTTP 代理转发

将本地 8080 端口的流量转发到远程 HTTP 代理服务器：

```json
{
  "endpoints": [
    {
      "local": "0.0.0.0:8080",
      "remote": "http-proxy.example.com:8080",
      "stats_enabled": true
    }
  ]
}
```

### SOCKS5 代理转发

将本地 1080 端口的流量转发到远程 SOCKS5 代理服务器：

```json
{
  "endpoints": [
    {
      "local": "0.0.0.0:1080",
      "remote": "socks5-server.example.com:1080",
      "stats_enabled": true
    }
  ]
}
```

### DNS 转发

将本地 DNS 查询转发到指定的 DNS 服务器：

```json
{
  "network": {
    "use_udp": true
  },
  "endpoints": [
    {
      "local": "0.0.0.0:53",
      "remote": "8.8.8.8:53",
      "stats_enabled": true
    }
  ]
}
```

### IPv6 迁移

支持 IPv4 到 IPv6 的转发：

```json
{
  "endpoints": [
    {
      "local": "0.0.0.0:8080",
      "remote": "[2001:db8::1]:80",
      "stats_enabled": true
    }
  ]
}
```

## 📁 项目结构

项目采用模块化设计，目录结构如下：

```
go-realm/
├── cmd/                  # 命令行工具
│   └── go-realm/         # 主程序入口
├── internal/             # 内部包
│   ├── config/           # 配置相关
│   ├── forwarder/        # 转发器核心逻辑
│   ├── logger/           # 日志功能
│   ├── pool/             # 连接池和缓冲区
│   ├── stats/            # 统计功能
│   └── zerocopy/         # 零拷贝实现
└── pkg/                  # 公共包
    └── utils/            # 工具函数
```

### 核心模块说明

- **cmd/go-realm**: 主程序入口，处理命令行参数和配置加载
- **internal/config**: 配置解析和验证
- **internal/forwarder**: 核心转发逻辑，处理 TCP/UDP 转发
- **internal/logger**: 日志记录和管理
- **internal/pool**: 连接池和缓冲区管理，优化连接复用
- **internal/stats**: 流量统计和监控
- **internal/zerocopy**: 零拷贝实现，提升传输效率
- **pkg/utils**: 通用工具函数，如格式化和网络辅助函数

## 🔧 高级特性

### 零拷贝优化

在 Linux 系统上，Go-Realm 可以使用零拷贝技术（splice 系统调用）来减少数据在内核和用户空间之间的复制，显著提升大流量场景下的性能。

启用零拷贝：

```json
"network": {
  "zero_copy": true
}
```

### 动态缓冲区

Go-Realm 实现了动态缓冲区机制，可以根据实际流量自动调整缓冲区大小，在保证性能的同时优化内存使用。

### 连接池管理

通过连接池机制，Go-Realm 可以复用到远程服务器的连接，减少连接建立的开销，提升高并发场景下的性能。

## ❓ 常见问题

### 性能相关

**Q: Go-Realm 的性能如何？**  
A: 在标准硬件上，单个 Go-Realm 实例可以轻松处理数千并发连接，在启用零拷贝的 Linux 系统上性能更佳。

**Q: 如何优化高并发场景？**  
A: 增加系统文件描述符限制，调整网络参数，并考虑使用零拷贝功能。

### 配置相关

**Q: 如何动态更新配置？**  
A: 目前需要重启服务来应用新配置，未来版本可能支持热重载。

**Q: 支持多少个转发端点？**  
A: 理论上没有硬性限制，但建议根据系统资源合理配置。

### 兼容性相关

**Q: 是否支持 Windows 系统？**  
A: 是的，Go-Realm 支持 Windows，但零拷贝功能仅在 Linux 系统上可用。

**Q: 是否支持 ARM 架构？**  
A: 是的，Go-Realm 可以在 ARM 处理器上编译和运行。

## 🤝 贡献指南

欢迎贡献代码、报告问题或提出改进建议！

1. Fork 本仓库
2. 创建您的特性分支 (`git checkout -b feature/amazing-feature`)
3. 提交您的更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 打开一个 Pull Request

### 代码风格

- 遵循 Go 标准代码风格
- 使用 `gofmt` 格式化代码
- 添加适当的注释和文档

## 📄 许可证

本项目采用 MIT 许可证 - 详见 [LICENSE](LICENSE) 文件。

## 🙏 致谢

- [realm](https://github.com/zhboner/realm) - 提供了原始设计思路和配置格式
- 所有贡献者和用户 - 感谢您的支持和反馈！
