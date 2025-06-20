package pool

import (
	"sync"
	"sync/atomic"
)

// BufferPool 表示一个 buffer 池，用于复用 buffer 以减少内存分配
type BufferPool struct {
	pool          sync.Pool // 底层 buffer 池
	size          int       // buffer 大小
	allocCount    int64     // 已分配的 buffer 数量
	recycleCount  int64     // 已回收的 buffer 数量
	currentActive int64     // 当前活跃的 buffer 数量
	maxActive     int64     // 历史最大活跃 buffer 数量
}

// NewBufferPool 创建一个新的 buffer 池
func NewBufferPool(size int) *BufferPool {
	return &BufferPool{
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, size)
			},
		},
		size: size,
	}
}

// Get 从池中获取一个 buffer
func (p *BufferPool) Get() []byte {
	atomic.AddInt64(&p.allocCount, 1)
	atomic.AddInt64(&p.currentActive, 1)

	// 更新最大活跃数
	for {
		current := atomic.LoadInt64(&p.maxActive)
		active := atomic.LoadInt64(&p.currentActive)
		if active <= current {
			break
		}
		if atomic.CompareAndSwapInt64(&p.maxActive, current, active) {
			break
		}
	}

	return p.pool.Get().([]byte)
}

// Put 将 buffer 放回池中
func (p *BufferPool) Put(buf []byte) {
	if len(buf) != p.size {
		// 如果 buffer 大小不匹配，创建新的
		buf = make([]byte, p.size)
	}
	atomic.AddInt64(&p.recycleCount, 1)
	atomic.AddInt64(&p.currentActive, -1)
	p.pool.Put(buf)
}

// BufferPoolStats 返回池的统计信息
type BufferPoolStats struct {
	Size          int   // buffer 大小
	AllocCount    int64 // 总分配次数
	RecycleCount  int64 // 总回收次数
	CurrentActive int64 // 当前活跃数量
	MaxActive     int64 // 历史最大活跃数量
}

// GetStats 获取池的统计信息
func (p *BufferPool) GetStats() BufferPoolStats {
	return BufferPoolStats{
		Size:          p.size,
		AllocCount:    atomic.LoadInt64(&p.allocCount),
		RecycleCount:  atomic.LoadInt64(&p.recycleCount),
		CurrentActive: atomic.LoadInt64(&p.currentActive),
		MaxActive:     atomic.LoadInt64(&p.maxActive),
	}
}

// BufferPoolManager 管理多个不同大小的 buffer 池
type BufferPoolManager struct {
	pools    map[int]*BufferPool
	poolsMap sync.Map
}

// NewBufferPoolManager 创建一个新的 buffer 池管理器
func NewBufferPoolManager() *BufferPoolManager {
	return &BufferPoolManager{
		pools: make(map[int]*BufferPool),
	}
}

// GetPool 获取指定大小的 buffer 池，如果不存在则创建
func (m *BufferPoolManager) GetPool(size int) *BufferPool {
	if pool, ok := m.poolsMap.Load(size); ok {
		return pool.(*BufferPool)
	}

	// 创建新池
	pool := NewBufferPool(size)
	m.poolsMap.Store(size, pool)
	return pool
}

// GetBuffer 从指定大小的池中获取 buffer
func (m *BufferPoolManager) GetBuffer(size int) []byte {
	return m.GetPool(size).Get()
}

// PutBuffer 将 buffer 放回对应大小的池中
func (m *BufferPoolManager) PutBuffer(buf []byte) {
	if buf == nil {
		return
	}
	if pool, ok := m.poolsMap.Load(len(buf)); ok {
		pool.(*BufferPool).Put(buf)
	}
}

// GetAllStats 获取所有池的统计信息
func (m *BufferPoolManager) GetAllStats() map[int]BufferPoolStats {
	stats := make(map[int]BufferPoolStats)
	m.poolsMap.Range(func(key, value interface{}) bool {
		size := key.(int)
		pool := value.(*BufferPool)
		stats[size] = pool.GetStats()
		return true
	})
	return stats
}
