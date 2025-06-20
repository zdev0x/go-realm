package main

import (
	"fmt"
	"sync"
	"time"
)

// BufferStats 记录缓冲区使用统计
type BufferStats struct {
	fullCount  int       // 缓冲区填满次数
	emptyCount int       // 缓冲区未填满次数
	avgUsage   float64   // 平均使用率
	totalBytes int64     // 总传输字节数
	lastAdjust time.Time // 上次调整时间
	mu         sync.Mutex
}

// DynamicBuffer 实现动态大小的缓冲区
type DynamicBuffer struct {
	buf         []byte
	minSize     int
	maxSize     int
	currentSize int
	stats       *BufferStats
	mu          sync.RWMutex
}

// NewDynamicBuffer 创建一个新的动态缓冲区
func NewDynamicBuffer(initialSize, minSize, maxSize int) *DynamicBuffer {
	if initialSize < minSize {
		initialSize = minSize
	}
	if initialSize > maxSize {
		initialSize = maxSize
	}

	return &DynamicBuffer{
		buf:         make([]byte, initialSize),
		minSize:     minSize,
		maxSize:     maxSize,
		currentSize: initialSize,
		stats: &BufferStats{
			lastAdjust: time.Now(),
		},
	}
}

// resize 调整缓冲区大小
func (b *DynamicBuffer) resize(newSize int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if newSize < b.minSize || newSize > b.maxSize {
		return
	}

	// 只有当新大小显著不同时才调整（避免频繁的小幅调整）
	if float64(newSize) < float64(b.currentSize)*0.75 || float64(newSize) > float64(b.currentSize)*1.25 {
		newBuf := make([]byte, newSize)
		copy(newBuf, b.buf)
		b.buf = newBuf
		b.currentSize = newSize
	}
}

// adjust 根据使用统计调整缓冲区大小
func (b *DynamicBuffer) adjust() {
	b.stats.mu.Lock()
	defer b.stats.mu.Unlock()

	// 计算平均使用率
	total := b.stats.fullCount + b.stats.emptyCount
	if total == 0 {
		return
	}

	// 计算使用率
	usageRatio := float64(b.stats.fullCount) / float64(total)
	b.stats.avgUsage = (b.stats.avgUsage*0.7 + usageRatio*0.3) // 指数移动平均

	// 根据使用率调整大小
	if usageRatio > 0.8 && b.currentSize < b.maxSize {
		// 缓冲区经常填满，需要增大
		newSize := min(b.currentSize*2, b.maxSize)
		b.resize(newSize)
	} else if usageRatio < 0.3 && b.currentSize > b.minSize {
		// 缓冲区经常未填满，可以减小
		newSize := max(b.currentSize/2, b.minSize)
		b.resize(newSize)
	}

	// 计算吞吐率
	elapsed := time.Since(b.stats.lastAdjust).Seconds()
	if elapsed > 0 {
		throughput := float64(b.stats.totalBytes) / elapsed
		// 根据吞吐率微调缓冲区
		if throughput > float64(b.currentSize)*10 {
			// 吞吐率很高，考虑增加缓冲区
			newSize := min(b.currentSize+b.currentSize/4, b.maxSize)
			b.resize(newSize)
		}
	}

	// 重置统计
	b.stats.fullCount = 0
	b.stats.emptyCount = 0
	b.stats.totalBytes = 0
	b.stats.lastAdjust = time.Now()
}

// Read 实现从缓冲区读取数据
func (b *DynamicBuffer) Read(src interface {
	Read([]byte) (int, error)
}) (int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	n, err := src.Read(b.buf)

	b.stats.mu.Lock()
	if n == len(b.buf) {
		b.stats.fullCount++
	} else {
		b.stats.emptyCount++
	}
	b.stats.totalBytes += int64(n)

	// 每1000次读取或10秒调整一次缓冲区大小
	total := b.stats.fullCount + b.stats.emptyCount
	if total%1000 == 0 || time.Since(b.stats.lastAdjust) > 10*time.Second {
		go b.adjust() // 异步调整，避免阻塞数据传输
	}
	b.stats.mu.Unlock()

	return n, err
}

// Write 实现向缓冲区写入数据
func (b *DynamicBuffer) Write(dst interface {
	Write([]byte) (int, error)
}, data []byte) (int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(data) > len(b.buf) {
		// 如果数据大于当前缓冲区，考虑增加缓冲区大小
		b.stats.mu.Lock()
		b.stats.fullCount++
		b.stats.totalBytes += int64(len(data))
		b.stats.mu.Unlock()
		go b.adjust() // 异步调整
	}

	return dst.Write(data)
}

// GetCurrentSize 获取当前缓冲区大小
func (b *DynamicBuffer) GetCurrentSize() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentSize
}

// GetStats 获取缓冲区统计信息
func (b *DynamicBuffer) GetStats() string {
	b.stats.mu.Lock()
	defer b.stats.mu.Unlock()

	total := b.stats.fullCount + b.stats.emptyCount
	usageRatio := 0.0
	if total > 0 {
		usageRatio = float64(b.stats.fullCount) / float64(total)
	}

	return fmt.Sprintf(
		"Buffer Stats: Size=%d, Usage=%.2f%%, Avg=%.2f%%, Total=%d, Full=%d, Empty=%d",
		b.currentSize,
		usageRatio*100,
		b.stats.avgUsage*100,
		total,
		b.stats.fullCount,
		b.stats.emptyCount,
	)
}

// GetBuffer 获取底层缓冲区
func (b *DynamicBuffer) GetBuffer() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.buf
}
