package audio

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/datarhei/gosrt"
	"github.com/yegors/co-atc/pkg/logger"
)

// SRTReader 使用原生 Go SRT 库从 SRT 流中读取音频数据。
// 它替代了用于 SRT 流的 ffmpeg,提供了更简洁的方案,
// 进程数更少。预期 SRT 流输出 WAV/PCM s16le 数据。
type SRTReader struct {
	url            string
	conn           srt.Conn
	ctx            context.Context
	cancel         context.CancelFunc
	logger         *logger.Logger
	mu             sync.Mutex
	isConnected    bool
	lastError      error
	reconnectDelay time.Duration
}

// SRTReaderConfig 包含 SRT 读取器的配置
type SRTReaderConfig struct {
	ReconnectDelay time.Duration
}

// NewSRTReader 为给定 URL 创建一个新的 SRT 读取器。
// URL 应该是 srt://host:port 的格式
func NewSRTReader(
	ctx context.Context,
	url string,
	config SRTReaderConfig,
	log *logger.Logger,
) *SRTReader {
	readerCtx, cancel := context.WithCancel(ctx)

	return &SRTReader{
		url:            url,
		ctx:            readerCtx,
		cancel:         cancel,
		logger:         log.Named("srt-reader"),
		reconnectDelay: config.ReconnectDelay,
	}
}

// parseSRTURL 解析 SRT URL 并返回 host:port
// 支持:srt://host:port 以及 srt://host:port?streamid=...
func parseSRTURL(url string) (address string, streamID string, err error) {
	// 移除 srt:// 前缀
	if !strings.HasPrefix(url, "srt://") {
		return "", "", fmt.Errorf("无效的 SRT URL:必须以 srt:// 开头")
	}

	rest := strings.TrimPrefix(url, "srt://")

	// 通过 ? 分隔地址与查询参数
	parts := strings.SplitN(rest, "?", 2)
	address = parts[0]

	// 解析查询参数中的 streamid
	if len(parts) > 1 {
		params := strings.Split(parts[1], "&")
		for _, param := range params {
			kv := strings.SplitN(param, "=", 2)
			if len(kv) == 2 && strings.ToLower(kv[0]) == "streamid" {
				streamID = kv[1]
				break
			}
		}
	}

	// 校验地址包含端口
	_, _, err = net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("无效的 SRT 地址 %q: %w", address, err)
	}

	return address, streamID, nil
}

// Connect 建立到 SRT 流的连接
func (r *SRTReader) Connect() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.isConnected {
		return nil
	}

	address, streamID, err := parseSRTURL(r.url)
	if err != nil {
		return fmt.Errorf("解析 SRT URL 失败: %w", err)
	}

	r.logger.Info("正在连接到 SRT 流",
		String("address", address),
		String("stream_id", streamID))

	// 为实时音频流配置 SRT 连接
	// rtl-airband 使用启用了 TSBPD 的 live 模式
	config := srt.DefaultConfig()
	config.TransmissionType = "live"

	if streamID != "" {
		config.StreamId = streamID
	}

	// 在拨号前校验配置
	if err := config.Validate(); err != nil {
		return fmt.Errorf("无效的 SRT 配置: %w", err)
	}

	r.logger.Debug("SRT 配置",
		String("transmission_type", config.TransmissionType),
		String("stream_id", config.StreamId))

	// 拨号连接 SRT 服务器
	conn, err := srt.Dial("srt", address, config)
	if err != nil {
		r.lastError = err
		return fmt.Errorf("连接到 SRT 流失败: %w", err)
	}

	r.conn = conn
	r.isConnected = true
	r.lastError = nil

	r.logger.Info("成功连接到 SRT 流",
		String("address", address))

	return nil
}

// Read 从 SRT 流读取数据
func (r *SRTReader) Read(p []byte) (n int, err error) {
	r.mu.Lock()
	if !r.isConnected || r.conn == nil {
		r.mu.Unlock()
		return 0, io.EOF
	}
	conn := r.conn
	r.mu.Unlock()

	// 检查上下文
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}

	n, err = conn.Read(p)
	if err != nil {
		r.mu.Lock()
		r.lastError = err
		r.isConnected = false
		r.mu.Unlock()

		if err == io.EOF {
			r.logger.Warn("SRT 流已结束")
		} else {
			r.logger.Error("从 SRT 流读取出错", Error(err))
		}
		return n, err
	}

	return n, nil
}

// Close 关闭 SRT 连接
func (r *SRTReader) Close() error {
	r.cancel()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn != nil {
		r.logger.Info("正在关闭 SRT 连接")
		err := r.conn.Close()
		r.conn = nil
		r.isConnected = false
		return err
	}

	return nil
}

// IsConnected 返回读取器当前是否已连接
func (r *SRTReader) IsConnected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.isConnected
}

// LastError 返回最后遇到的错误
func (r *SRTReader) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastError
}
