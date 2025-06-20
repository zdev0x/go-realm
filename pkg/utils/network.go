package utils

import (
	"io"
	"net"
	"strings"
	"time"
)

// IsConnectionError 检查是否为连接相关错误
func IsConnectionError(err error) bool {
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

// ValidateConnection 验证连接是否有效
func ValidateConnection(conn *net.TCPConn) error {
	if conn == nil {
		return io.ErrUnexpectedEOF
	}

	// 设置一个极短的写入超时，用于探测连接是否可写
	// 零字节写入是一个轻量级的探测方式，不会实际发送数据
	// 但如果连接已断开（例如 RST），它会立即返回错误
	deadline := time.Now().Add(time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	// 尝试写入0字节数据来探测连接状态
	if _, err := conn.Write([]byte{}); err != nil {
		return err
	}

	// 重置写入期限
	return conn.SetWriteDeadline(time.Time{})
}
