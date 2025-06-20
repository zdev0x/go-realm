package logger

import (
	"fmt"
	"log"
	"os"
	"sync"
)

// LogConfig 定义日志设置
type LogConfig struct {
	Level  string `json:"level"`  // Log level (e.g., "warn")
	Output string `json:"output"` // Log output file (e.g., "realm.log")
}

// AsyncLogger 实现异步日志记录器
type AsyncLogger struct {
	logChan chan string
	wg      sync.WaitGroup
}

// NewAsyncLogger 创建一个新的异步日志记录器
func NewAsyncLogger(bufferSize int) *AsyncLogger {
	logger := &AsyncLogger{
		logChan: make(chan string, bufferSize),
	}

	logger.wg.Add(1)
	go func() {
		defer logger.wg.Done()
		for msg := range logger.logChan {
			log.Print(msg)
		}
	}()

	return logger
}

// Printf 异步打印日志
func (l *AsyncLogger) Printf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	select {
	case l.logChan <- msg:
		// ok
	default:
		// 日志通道满时丢弃，防止阻塞主流程
	}
}

// Close 关闭日志记录器
func (l *AsyncLogger) Close() {
	close(l.logChan)
	l.wg.Wait()
}

// ConfigureLogging 配置日志输出
func ConfigureLogging(logConfig LogConfig) {
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
