package audio

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// MultiReader 实现一个可被多个 goroutine 消费的读取器
type MultiReader struct {
	buffer     []byte // 用于音频数据的环形缓冲区
	bufferSize int    // 环形缓冲区的大小
	writeIndex int    // 缓冲区中当前的写入位置
	readers    map[string]*readerState
	mu         sync.RWMutex // 用于线程安全的互斥锁
	ctx        context.Context
	cancel     context.CancelFunc
	logger     *logger.Logger
	closed     bool
}

// readerState 跟踪每个读取器的状态
type readerState struct {
	readIndex int        // 环形缓冲区中当前的读取位置
	readCond  *sync.Cond // 用于通知有新数据的条件变量
	closed    bool
}

// NewMultiReader 创建一个新的多读取器
func NewMultiReader(ctx context.Context, logger *logger.Logger) *MultiReader {
	bufferSize := 1024 * 64 // 64KB 缓冲区,用于低延迟(在 24kHz 单声道下约为 1.3 秒)
	readerCtx, readerCancel := context.WithCancel(ctx)

	mr := &MultiReader{
		buffer:     make([]byte, bufferSize),
		bufferSize: bufferSize,
		writeIndex: 0,
		readers:    make(map[string]*readerState),
		ctx:        readerCtx,
		cancel:     readerCancel,
		logger:     logger,
		closed:     false,
	}

	return mr
}

// Write 将数据写入缓冲区并通知所有读取器
func (mr *MultiReader) Write(p []byte) (n int, err error) {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if mr.closed {
		return 0, io.ErrClosedPipe
	}

	// 将数据复制到环形缓冲区
	n = len(p)
	for i := 0; i < n; i++ {
		mr.buffer[mr.writeIndex] = p[i]
		mr.writeIndex = (mr.writeIndex + 1) % mr.bufferSize
	}

	// 通知所有读取器有新数据可用
	for _, reader := range mr.readers {
		if !reader.closed && reader.readCond != nil {
			reader.readCond.Signal()
		}
	}

	return n, nil
}

// CreateReader 为多读取器创建一个新的读取器
func (mr *MultiReader) CreateReader(id string) io.ReadCloser {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	// 检查读取器是否已存在
	if reader, exists := mr.readers[id]; exists {
		if !reader.closed {
			return newMultiReaderClient(mr, id)
		}
		// 如果存在但已关闭,先移除并创建一个新的
		delete(mr.readers, id)
	}

	// 创建新的读取器状态
	readerMutex := &sync.Mutex{}
	reader := &readerState{
		readIndex: mr.writeIndex,             // 从当前写入位置开始读取
		readCond:  sync.NewCond(readerMutex), // 用于通知的条件变量
		closed:    false,
	}

	mr.readers[id] = reader
	mr.logger.Debug("已创建新的读取器", logger.String("reader_id", id))

	return newMultiReaderClient(mr, id)
}

// RemoveReader 移除一个读取器
func (mr *MultiReader) RemoveReader(id string) {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if reader, exists := mr.readers[id]; exists {
		// 标记为已关闭
		reader.closed = true

		// 唤醒可能正在等待的读取器
		if reader.readCond != nil {
			reader.readCond.Signal()
		}

		// 从 map 中移除
		delete(mr.readers, id)
		mr.logger.Debug("已移除读取器", logger.String("reader_id", id))
	}
}

// Close 关闭多读取器及其所有读取器
func (mr *MultiReader) Close() error {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if mr.closed {
		return nil
	}

	mr.closed = true
	mr.cancel()

	// 关闭所有读取器
	for id, reader := range mr.readers {
		// 标记为已关闭
		reader.closed = true

		// 唤醒可能正在等待的读取器
		if reader.readCond != nil {
			reader.readCond.Signal()
		}

		mr.logger.Debug("关闭过程中已关闭读取器", logger.String("reader_id", id))
	}

	// 清空读取器 map
	mr.readers = make(map[string]*readerState)

	return nil
}

// multiReaderClient 是从 MultiReader 中读取数据的 ReadCloser
type multiReaderClient struct {
	mr *MultiReader
	id string
	mu sync.Mutex // 用于线程安全的互斥锁
}

// newMultiReaderClient 为多读取器创建一个新的客户端
func newMultiReaderClient(mr *MultiReader, id string) io.ReadCloser {
	return &multiReaderClient{
		mr: mr,
		id: id,
	}
}

// Read 从多读取器中读取数据
func (mrc *multiReaderClient) Read(p []byte) (n int, err error) {
	// 加锁以防止同一客户端的并发读取
	mrc.mu.Lock()
	defer mrc.mu.Unlock()

	// 获取读取器状态
	mrc.mr.mu.RLock()
	reader, exists := mrc.mr.readers[mrc.id]
	if !exists || reader.closed || mrc.mr.closed {
		mrc.mr.mu.RUnlock()
		return 0, io.EOF
	}

	// 获取当前读取位置和缓冲区大小
	readIndex := reader.readIndex
	writeIndex := mrc.mr.writeIndex
	bufferSize := mrc.mr.bufferSize
	readCond := reader.readCond
	mrc.mr.mu.RUnlock()

	// 如果没有可用数据,等待
	if readIndex == writeIndex {
		// 带超时地等待数据
		waitChan := make(chan struct{})

		go func() {
			readCond.L.Lock()
			defer readCond.L.Unlock()

			// 等待信号或超时
			readCond.Wait()
			close(waitChan)
		}()

		// 等待数据或上下文取消
		select {
		case <-waitChan:
			// 数据已可用,继续
		case <-mrc.mr.ctx.Done():
			return 0, io.EOF
		case <-time.After(30 * time.Second):
			// 较长的超时,返回 EOF 以提示需要重新建立连接
			return 0, io.EOF
		}

		// 等待之后重新检查状态
		mrc.mr.mu.RLock()
		reader, exists = mrc.mr.readers[mrc.id]
		if !exists || reader.closed || mrc.mr.closed {
			mrc.mr.mu.RUnlock()
			return 0, io.EOF
		}
		readIndex = reader.readIndex
		writeIndex = mrc.mr.writeIndex
		mrc.mr.mu.RUnlock()
	}

	// 计算可用数据量
	var available int
	if writeIndex > readIndex {
		available = writeIndex - readIndex
	} else {
		available = bufferSize - readIndex + writeIndex
	}

	// 限制为缓冲区大小
	if available > len(p) {
		available = len(p)
	}

	// 将数据从环形缓冲区复制到输出缓冲区
	copied := 0
	for copied < available {
		// 计算连续块的大小
		chunkSize := available - copied
		if readIndex+chunkSize > bufferSize {
			chunkSize = bufferSize - readIndex
		}

		// 加锁以从缓冲区读取
		mrc.mr.mu.RLock()
		copy(p[copied:copied+chunkSize], mrc.mr.buffer[readIndex:readIndex+chunkSize])
		mrc.mr.mu.RUnlock()

		copied += chunkSize
		readIndex = (readIndex + chunkSize) % bufferSize
	}

	// 更新读取位置
	mrc.mr.mu.Lock()
	if reader, exists := mrc.mr.readers[mrc.id]; exists && !reader.closed {
		reader.readIndex = readIndex
	}
	mrc.mr.mu.Unlock()

	return copied, nil
}

// Close 关闭读取器
func (mrc *multiReaderClient) Close() error {
	mrc.mr.RemoveReader(mrc.id)
	return nil
}
