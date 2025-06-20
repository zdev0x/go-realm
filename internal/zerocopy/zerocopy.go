package zerocopy

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// Linux splice flags
const (
	SPLICE_F_MOVE     = 0x01
	SPLICE_F_NONBLOCK = 0x02
)

// TCPSplice 使用零拷贝技术在两个TCP连接之间传输数据
func TCPSplice(src, dst *net.TCPConn, idleTimeout time.Duration) error {
	// 获取文件描述符
	srcFile, err := src.File()
	if err != nil {
		return fmt.Errorf("获取源连接文件描述符失败: %v", err)
	}
	defer srcFile.Close()

	dstFile, err := dst.File()
	if err != nil {
		return fmt.Errorf("获取目标连接文件描述符失败: %v", err)
	}
	defer dstFile.Close()

	// 获取原始文件描述符
	srcFd := int(srcFile.Fd())
	dstFd := int(dstFile.Fd())

	// 设置为非阻塞模式
	if err := syscall.SetNonblock(srcFd, true); err != nil {
		return fmt.Errorf("设置源连接为非阻塞模式失败: %v", err)
	}
	if err := syscall.SetNonblock(dstFd, true); err != nil {
		return fmt.Errorf("设置目标连接为非阻塞模式失败: %v", err)
	}

	// 准备轮询
	var events [2]syscall.EpollEvent
	events[0].Events = syscall.EPOLLIN
	events[0].Fd = int32(srcFd)
	events[1].Events = syscall.EPOLLIN
	events[1].Fd = int32(dstFd)

	// 创建epoll实例
	epollFd, err := syscall.EpollCreate1(0)
	if err != nil {
		return fmt.Errorf("创建epoll实例失败: %v", err)
	}
	defer syscall.Close(epollFd)

	// 添加文件描述符到epoll
	if err := syscall.EpollCtl(epollFd, syscall.EPOLL_CTL_ADD, srcFd, &events[0]); err != nil {
		return fmt.Errorf("添加源连接到epoll失败: %v", err)
	}
	if err := syscall.EpollCtl(epollFd, syscall.EPOLL_CTL_ADD, dstFd, &events[1]); err != nil {
		return fmt.Errorf("添加目标连接到epoll失败: %v", err)
	}

	// 准备接收事件
	const maxEvents = 10
	epollEvents := make([]syscall.EpollEvent, maxEvents)

	// 设置超时时间
	timeout := idleTimeout.Milliseconds()
	if timeout < 0 {
		timeout = -1 // 永不超时
	}

	// 开始零拷贝转发
	for {
		// 等待事件
		n, err := syscall.EpollWait(epollFd, epollEvents, int(timeout))
		if err != nil {
			if err == syscall.EINTR {
				continue // 系统调用被中断，重试
			}
			return fmt.Errorf("epoll等待失败: %v", err)
		}

		// 检查超时
		if n == 0 {
			return fmt.Errorf("连接空闲超时")
		}

		// 处理事件
		for i := 0; i < n; i++ {
			if epollEvents[i].Fd == int32(srcFd) {
				// 源连接可读，将数据从源发送到目标
				if err := spliceFd(srcFd, dstFd); err != nil {
					if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
						continue // 资源暂时不可用，重试
					}
					return fmt.Errorf("从源到目标的零拷贝失败: %v", err)
				}
			} else if epollEvents[i].Fd == int32(dstFd) {
				// 目标连接可读，将数据从目标发送到源
				if err := spliceFd(dstFd, srcFd); err != nil {
					if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
						continue // 资源暂时不可用，重试
					}
					return fmt.Errorf("从目标到源的零拷贝失败: %v", err)
				}
			}
		}
	}
}

// spliceFd 使用splice系统调用在两个文件描述符之间直接传输数据
func spliceFd(inFd, outFd int) error {
	// 创建管道
	var pipefd [2]int
	if err := syscall.Pipe(pipefd[:]); err != nil {
		return err
	}
	defer syscall.Close(pipefd[0])
	defer syscall.Close(pipefd[1])

	// 设置管道为非阻塞模式
	if err := syscall.SetNonblock(pipefd[0], true); err != nil {
		return err
	}
	if err := syscall.SetNonblock(pipefd[1], true); err != nil {
		return err
	}

	// 从输入fd拷贝到管道
	n, err := syscall.Splice(inFd, nil, pipefd[1], nil, 65536, SPLICE_F_MOVE|SPLICE_F_NONBLOCK)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("连接已关闭")
	}

	// 从管道拷贝到输出fd
	_, err = syscall.Splice(pipefd[0], nil, outFd, nil, int(n), SPLICE_F_MOVE|SPLICE_F_NONBLOCK)
	return err
}

// IsZeroCopySupported 检查当前系统是否支持零拷贝
func IsZeroCopySupported() bool {
	// 创建测试管道
	var pipefd [2]int
	err := syscall.Pipe(pipefd[:])
	if err != nil {
		return false
	}
	defer syscall.Close(pipefd[0])
	defer syscall.Close(pipefd[1])

	// 尝试使用splice系统调用
	_, err = syscall.Splice(pipefd[0], nil, pipefd[1], nil, 0, 0)
	return err == nil || err == syscall.EAGAIN
}
