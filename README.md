项目功能说明
本项目是一个基于 Go 语言开发的网络流量转发工具，参考 realm (https://github.com/zhboner/realm) 的配置格式和功能设计，旨在提供高效、可靠的 TCP 和 UDP 流量转发服务。项目支持 IPv6 转发、域名解析、透明转发（无需解析上层协议如 SOCKS5 或 HTTP）、限速以及流量统计功能，适用于多种网络场景，如代理服务、端口转发和网络调试。以下是项目的详细功能说明。
1. 核心功能
1.1 流量转发

TCP 转发：
支持监听本地 TCP 端口（如 [::]:8080 或 0.0.0.0:8080），将数据透明转发到目标地址（IP 或域名，如 example.com:80 或 [2001:db8::1]:80）。
实现双向数据传输，适用于 HTTP、SOCKS5 等协议。


UDP 转发：
支持监听本地 UDP 端口（如 [::]:53），将数据报转发到目标地址（如 [2001:db8::1]:53）。
支持双向 UDP 数据交换，适用于 DNS 查询等场景。


透明转发：
不解析上层协议（如 SOCKS5、HTTP），直接转发原始数据流，客户端和目标服务器负责协议处理。
兼容多种应用层协议，降低程序复杂度。



1.2 IPv6 支持

监听端：支持在 IPv6 地址（如 [::]:8080）上监听 TCP 和 UDP。
转发端：支持连接到 IPv6 目标地址（如 [2001:db8::1]:80）或通过域名解析获得的 IPv6 地址。
地址格式：正确处理 IPv6 地址格式（如 [ip]:port），确保兼容性。

1.3 域名解析

DNS 配置：
支持自定义 DNS 服务器（支持 IPv4 和 IPv6，如 8.8.8.8:53 和 [2001:4860:4860::8888]:53）。
支持 DNS 查询协议（tcp_and_udp，默认优先 UDP）。


解析模式：
ipv4_only：仅解析 IPv4 地址。
ipv6_only：仅解析 IPv6 地址。
ipv4_and_ipv6：优先解析 IPv6 地址，若无则回退到 IPv4。


DNS 缓存：
使用 github.com/hashicorp/golang-lru 实现 DNS 解析结果缓存，减少重复查询。
支持配置缓存大小（cache_size）和 TTL（min_ttl 和 max_ttl）。



1.4 限速功能

带宽限制：为每个转发端点（endpoint）配置限速（rate_limit_bps），单位为字节每秒。
实现方式：使用 golang.org/x/time/rate 库，在 TCP 和 UDP 连接上实现精准的流量控制。
适用场景：防止流量过载，优化带宽分配。

1.5 流量统计

统计内容：记录每个端点的入站（BytesIn）和出站（BytesOut）流量，以及最后更新时间（LastUpdated）。
启用方式：通过 stats_enabled 字段控制是否启用统计。
日志输出：每 10 秒输出一次统计信息到日志文件（如 realm.log），格式为：Stats for [::]:8080 -> example.com:80: 1024 bytes in, 2048 bytes out, last updated: 2025-06-20 15:45:50



1.6 日志管理

日志级别：支持 warn 级别，记录关键错误和运行状态。
输出位置：支持输出到文件（如 realm.log）或标准输出。
日志内容：包括监听状态、连接错误、流量统计等，便于调试和监控。

1.7 配置文件

格式：采用 JSON 格式，与 realm 配置兼容，包含以下部分：
log：日志配置（级别、输出文件）。
network：网络设置（是否禁用 TCP、是否启用 UDP）。
dns：DNS 配置（模式、协议、服务器、TTL、缓存大小）。
endpoints：转发规则（本地地址、目标地址、限速、统计开关）。


示例：{
    "log": {
        "level": "warn",
        "output": "realm.log"
    },
    "network": {
        "no_tcp": false,
        "use_udp": true
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
            "local": "[::]:8080",
            "remote": "example.com:80",
            "rate_limit_bps": 1024000,
            "stats_enabled": true
        },
        {
            "local": "[::]:1080",
            "remote": "socks5.example.com:2008",
            "rate_limit_bps": 1024000,
            "stats_enabled": true
        },
        {
            "local": "[::]:53",
            "remote": "[2001:db8::1]:53",
            "rate_limit_bps": 512000,
            "stats_enabled": true
        }
    ]
}



2. 高级功能
2.1 错误处理与鲁棒性

连接重试：当连接目标服务器失败时，最多重试 3 次，采用指数退避（1s、2s、3s）。
超时管理：为目标 TCP 连接设置 30 秒读写超时，防止因服务器无响应而挂起。
错误抑制：忽略常见的 broken pipe 错误（连接被远程关闭），仅记录关键错误，减少日志冗余。
增强日志：记录客户端地址和端点信息，便于定位问题。

2.2 协议转发

协议选择：通过 network.no_tcp 和 network.use_udp 配置是否启用 TCP 和/或 UDP 转发。
动态地址解析：支持直接使用 IP 地址（IPv4 或 IPv6）或通过域名解析获取目标地址。

2.3 性能优化

DNS优化缓存：减少 DNS 查询开销，提升转发性能。
并发处理：每个端点在独立 goroutine 中运行，支持高并发连接。
限速精度：使用 rate.Limiter 确保流量控制的精确性。

3. 使用场景

代理服务：
支持 SOCKS5 代理（如 curl --socks5 [::1]:1080），客户端处理协议握手，程序透明转发。
适用于 HTTP 代理场景（如 curl http://[::1]:8080）。


端口转发：
将本地端口（如 [::]:53）的流量转发到远程服务器（如 DNS 服务器 [::1]:8086）。


网络调试：
通过流量统计和日志，监控网络流量和流量状态，适合调试网络应用。


IPv6 迁移：
支持 IPv6 监听和转发，助力网络从 IPv4 向 IPv6 过渡。



4. 依赖与环境要求

Go 版本：兼容 Go 1.16 及以上。
依赖库：
golang.org/x/time/rate：用于限速。
github.com/hashicorp/golang-lru：用于 DNS 缓存。


网络环境：
支持 IPv4/IPv6 地址的网络接口。
可访问的 DNS 服务器（如 8.8.8.8 或 [2001:4860:4860::8888）。


操作系统：跨平台支持（Linux、Windows、macOS）。

5. 测试方法

安装依赖：go get golang.org/x/time/rate
go get github.com/hashicorp/golang-lru


保存配置文件：
将 config.json 保存到项目目录。


运行程序：go run forwarder.go


测试用例：
HTTP 转发：curl http://[::1]:8080


转发到 example.com:80。


SOCKS5 转发：curl --socks5 [::1]:1080 http://example.com


转发到 socks5.example.com:2008。


UDP 转发（DNS）：dig @::1 -p 53 example.com


转发到 [2001:db8::1]:53。




检查日志：
查看 realm.log 中的监听状态、错误和流量统计。



6. 注意事项

IPv6 环境：确保系统启用 IPv6，且目标服务器支持 IPv6（若使用 ipv6_only 或 ipv4_and_ipv6 模式）。
SOCKS5 兼容性：客户端需处理 SOCKS5 握手，程序仅透明转发。
DNS 配置：确保 nameservers 中的 DNS 服务器可用，建议包含 IPv6 服务器。
限速设置：合理配置 rate_limit_bps，避免过低导致性能瓶颈。
日志管理：定期清理 realm.log，防止占用过多磁盘空间。

7. 可能的扩展方向

动态配置重载：支持运行时重新加载 config.json，无需重启程序。
TLS 支持：为 HTTPS 或加密 SOCKS5 添加 TLS 连接。
更细粒度的日志级别：支持 debug、info 等级别，增强调试能力。
动态 DNS 更新：定期重新解析域名，刷新缓存。
高级错误处理：区分客户端和服务器端连接关闭，提供更精准的错误报告。

8. 项目优势

与 realm 兼容：配置文件格式一致，便于迁移和集成。
IPv6 全面支持：覆盖监听、转发和 DNS 解析，适应未来网络趋势。
透明转发：无需解析上层协议，兼容性强，性能高。
限速与统计：提供带宽控制和流量监控，适合生产环境。
鲁棒性：通过重试机制、超时管理和错误抑制，确保稳定运行。

9. 总结
本项目是一个功能强大、灵活的网络转发工具，支持 IPv6 和 IPv4 的 TCP/UDP 流量转发，具备域名解析、限速、流量统计和日志管理功能。程序设计参考 realm，在保持兼容性的同时增强了 IPv6 支持和错误处理能力，适用于代理、端口转发和网络调试等多种场景。如需进一步优化或扩展功能（如 TLS 或动态配置），请提供具体需求！
