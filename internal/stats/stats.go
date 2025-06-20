package stats

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Stats 跟踪端点的流量统计
type Stats struct {
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
	LastUpdated time.Time `json:"last_updated"`
	// 计算速率的字段
	BytesInPerSecond  float64   `json:"bytes_in_per_second"`
	BytesOutPerSecond float64   `json:"bytes_out_per_second"`
	LastBytesIn       int64     `json:"-"`
	LastBytesOut      int64     `json:"-"`
	LastCalcTime      time.Time `json:"-"`
	// 连接统计
	TotalConnections  int64 `json:"total_connections"`
	ActiveConnections int32 `json:"active_connections"`
}

// StatsManager 管理统计数据
type StatsManager struct {
	Stats     *Stats
	StatsLock sync.Mutex
	Enabled   bool
}

// NewStatsManager 创建一个新的统计管理器
func NewStatsManager(enabled bool) *StatsManager {
	return &StatsManager{
		Stats: &Stats{
			LastUpdated: time.Now(),
		},
		Enabled: enabled,
	}
}

// UpdateStats 安全地更新流量统计
func (sm *StatsManager) UpdateStats(bytesTransferred int64, isInbound bool) {
	if !sm.Enabled {
		return
	}

	sm.StatsLock.Lock()
	defer sm.StatsLock.Unlock()

	now := time.Now()

	// 更新总字节数
	if isInbound {
		sm.Stats.BytesIn += bytesTransferred
	} else {
		sm.Stats.BytesOut += bytesTransferred
	}

	// 计算速率（每秒）
	if !sm.Stats.LastCalcTime.IsZero() {
		duration := now.Sub(sm.Stats.LastCalcTime).Seconds()
		if duration >= 1.0 { // 至少1秒才更新速率
			if isInbound {
				bytesPerSecond := float64(sm.Stats.BytesIn-sm.Stats.LastBytesIn) / duration
				sm.Stats.BytesInPerSecond = bytesPerSecond
				sm.Stats.LastBytesIn = sm.Stats.BytesIn
			} else {
				bytesPerSecond := float64(sm.Stats.BytesOut-sm.Stats.LastBytesOut) / duration
				sm.Stats.BytesOutPerSecond = bytesPerSecond
				sm.Stats.LastBytesOut = sm.Stats.BytesOut
			}
			sm.Stats.LastCalcTime = now
		}
	} else {
		// 首次更新
		sm.Stats.LastCalcTime = now
		if isInbound {
			sm.Stats.LastBytesIn = sm.Stats.BytesIn
		} else {
			sm.Stats.LastBytesOut = sm.Stats.BytesOut
		}
	}

	sm.Stats.LastUpdated = now
}

// IncrementConnections 增加连接计数
func (sm *StatsManager) IncrementConnections() {
	if !sm.Enabled {
		return
	}

	sm.StatsLock.Lock()
	sm.Stats.TotalConnections++
	sm.StatsLock.Unlock()
	atomic.AddInt32(&sm.Stats.ActiveConnections, 1)
}

// DecrementActiveConnections 减少活跃连接计数
func (sm *StatsManager) DecrementActiveConnections() {
	if !sm.Enabled {
		return
	}

	atomic.AddInt32(&sm.Stats.ActiveConnections, -1)
}

// FormatBytes converts bytes to human readable format
func FormatBytes(bytes int64) string {
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

// FormatBytesPerSecond converts bytes per second to human readable format
func FormatBytesPerSecond(bytesPerSecond float64) string {
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

// GetFormattedStats 获取格式化的统计信息
func (sm *StatsManager) GetFormattedStats(local, remote string, isZeroCopy bool) string {
	if !sm.Enabled {
		return ""
	}

	sm.StatsLock.Lock()
	stats := *sm.Stats // 复制统计数据以减少锁定时间
	sm.StatsLock.Unlock()

	// 格式化速率显示
	inRate := FormatBytesPerSecond(stats.BytesInPerSecond)
	outRate := FormatBytesPerSecond(stats.BytesOutPerSecond)
	totalIn := FormatBytes(stats.BytesIn)
	totalOut := FormatBytes(stats.BytesOut)

	totalConns := strconv.FormatInt(stats.TotalConnections, 10)
	activeConns := strconv.FormatInt(int64(atomic.LoadInt32(&stats.ActiveConnections)), 10)

	if isZeroCopy {
		return "[ZeroCopy] Stats for " + local + " -> " + remote + ":\n" +
			"  Total: " + totalIn + " in, " + totalOut + " out\n" +
			"  Rate: " + inRate + "/s in, " + outRate + "/s out\n" +
			"  Connections: " + totalConns + " total, " + activeConns + " active\n" +
			"  Last updated: " + stats.LastUpdated.Format("2006-01-02 15:04:05") + "\n" +
			"  [警告] 零拷贝模式下统计仅供参考，可能不准确"
	}

	return "Stats for " + local + " -> " + remote + ":\n" +
		"  Total: " + totalIn + " in, " + totalOut + " out\n" +
		"  Rate: " + inRate + "/s in, " + outRate + "/s out\n" +
		"  Connections: " + totalConns + " total, " + activeConns + " active\n" +
		"  Last updated: " + stats.LastUpdated.Format("2006-01-02 15:04:05")
}
