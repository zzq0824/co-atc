package audio

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// 引入 logger 函数
var (
	String = logger.String
	Int    = logger.Int
	Error  = logger.Error
)

// sourceType 指示正在使用哪种音频源后端
type sourceType int

const (
	sourceTypeFFmpeg sourceType = iota // 用于 HTTP 流的 ffmpeg 子进程
	sourceTypeSRT                      // 用于 srt:// URL 的原生 SRT
)

// ConnectionStatus 表示频率的当前连接状态
type ConnectionStatus string

const (
	StatusConnecting ConnectionStatus = "connecting"
	StatusConnected  ConnectionStatus = "connected"
	StatusFailed     ConnectionStatus = "failed"
	StatusStopped    ConnectionStatus = "stopped"
)

// StatusChangeCallback 在连接状态变更时被调用
type StatusChangeCallback func(frequencyID string, status ConnectionStatus, errorMsg string)

// CentralAudioProcessor 管理某个频率的音频处理,
// 该频率可以在浏览器流式播放与转写之间共享。
// 它会自动对 srt:// URL 使用原生 SRT,对 HTTP 流使用 ffmpeg。
type CentralAudioProcessor struct {
	id                       string
	audioURL                 string
	ffmpegPath               string
	sampleRate               int
	channels                 int
	ffmpegTimeoutSecs        int // FFmpeg 连接超时(秒)
	ffmpegReconnectDelaySecs int // FFmpeg 重连延迟(秒)
	ffmpegCmd                *exec.Cmd
	ffmpegStdout             io.ReadCloser
	srtReader                *SRTReader // 原生 SRT 读取器(对 srt:// 使用,而非 ffmpeg)
	sourceType               sourceType // 正在使用的后端
	multiReader              *MultiReader
	ctx                      context.Context
	cancel                   context.CancelFunc
	logger                   *logger.Logger
	mu                       sync.Mutex
	isRunning                bool
	lastError                error
	lastActivity             time.Time
	reconnectTimer           *time.Timer
	monitorTicker            *time.Ticker
	reconnectDelay           time.Duration
	format                   string
	contentType              string
	statusCallback           StatusChangeCallback // 状态变更回调
	currentStatus            ConnectionStatus     // 当前连接状态
}

// CentralProcessorConfig 包含中央音频处理器的配置
type CentralProcessorConfig struct {
	FFmpegPath               string
	SampleRate               int
	Channels                 int
	Format                   string
	ReconnectDelay           time.Duration
	FFmpegTimeoutSecs        int // FFmpeg 连接超时(秒,0 = 无超时)
	FFmpegReconnectDelaySecs int // FFmpeg 重连延迟(秒)
}

// NewCentralAudioProcessor 创建一个新的中央音频处理器。
// 对于 srt:// URL,它使用原生 Go SRT 库而不是 ffmpeg。
func NewCentralAudioProcessor(
	ctx context.Context,
	id string,
	audioURL string,
	config CentralProcessorConfig,
	logger *logger.Logger,
) (*CentralAudioProcessor, error) {
	procCtx, procCancel := context.WithCancel(ctx)

	// 创建用于共享流的多读取器
	multiReader := NewMultiReader(procCtx, logger.Named("multi-reader"))

	// 根据 URL 确定源类型
	srcType := sourceTypeFFmpeg
	if strings.HasPrefix(audioURL, "srt://") {
		srcType = sourceTypeSRT
	}

	return &CentralAudioProcessor{
		id:                       id,
		audioURL:                 audioURL,
		ffmpegPath:               config.FFmpegPath,
		sampleRate:               config.SampleRate,
		channels:                 config.Channels,
		ffmpegTimeoutSecs:        config.FFmpegTimeoutSecs,
		ffmpegReconnectDelaySecs: config.FFmpegReconnectDelaySecs,
		sourceType:               srcType,
		multiReader:              multiReader,
		ctx:                      procCtx,
		cancel:                   procCancel,
		logger:                   logger.Named("central-audio-processor").With(String("id", id)),
		isRunning:                false,
		lastActivity:             time.Now(),
		contentType:              "audio/wav", // 我们将提供 WAV 格式
		format:                   config.Format,
		reconnectDelay:           config.ReconnectDelay,
	}, nil
}

// SetStatusCallback 设置状态变更的回调函数
func (p *CentralAudioProcessor) SetStatusCallback(callback StatusChangeCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCallback = callback
}

// notifyStatusChange 通知监听者状态变更
func (p *CentralAudioProcessor) notifyStatusChange(status ConnectionStatus, errorMsg string) {
	p.currentStatus = status
	if p.statusCallback != nil {
		// 在 goroutine 中调用回调以避免阻塞
		go p.statusCallback(p.id, status, errorMsg)
	}
}

// Start 启动音频处理器
func (p *CentralAudioProcessor) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isRunning {
		return nil
	}

	p.logger.Info("正在启动中央音频处理器",
		String("url", p.audioURL),
		Int("sample_rate", p.sampleRate),
		Int("channels", p.channels),
		String("source_type", p.sourceTypeString()))

	// 通知正在连接状态
	p.notifyStatusChange(StatusConnecting, "")

	// 根据 URL 类型启动相应的源
	var err error
	if p.sourceType == sourceTypeSRT {
		err = p.startSRT()
		if err != nil {
			p.notifyStatusChange(StatusFailed, err.Error())
			return fmt.Errorf("启动原生 SRT 失败: %w", err)
		}
	} else {
		err = p.startFFmpeg()
		if err != nil {
			p.notifyStatusChange(StatusFailed, err.Error())
			return fmt.Errorf("启动 ffmpeg 失败: %w", err)
		}
	}

	// 启动监控
	p.startMonitoring()

	p.isRunning = true
	// 通知已连接状态
	p.notifyStatusChange(StatusConnected, "")
	return nil
}

// sourceTypeString 返回源类型的人类可读字符串
func (p *CentralAudioProcessor) sourceTypeString() string {
	switch p.sourceType {
	case sourceTypeSRT:
		return "native-srt"
	case sourceTypeFFmpeg:
		return "ffmpeg"
	default:
		return "unknown"
	}
}

// Stop 停止音频处理器
func (p *CentralAudioProcessor) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isRunning {
		return nil
	}

	p.logger.Info("正在停止中央音频处理器",
		String("source_type", p.sourceTypeString()))

	// 停止监控
	if p.monitorTicker != nil {
		p.monitorTicker.Stop()
		p.monitorTicker = nil
	}

	// 取消上下文以停止所有操作
	p.cancel()

	// 停止相应的源
	if p.sourceType == sourceTypeSRT {
		p.stopSRT()
	} else {
		p.stopFFmpeg()
	}

	// 关闭多读取器
	p.multiReader.Close()

	p.isRunning = false
	// 通知已停止状态
	p.notifyStatusChange(StatusStopped, "")
	return nil
}

// startSRT 启动原生 SRT 读取器
func (p *CentralAudioProcessor) startSRT() error {
	p.logger.Debug("正在启动原生 SRT 读取器",
		String("url", p.audioURL))

	// 创建 SRT 读取器
	p.srtReader = NewSRTReader(p.ctx, p.audioURL, SRTReaderConfig{
		ReconnectDelay: p.reconnectDelay,
	}, p.logger)

	// 连接到 SRT 流
	if err := p.srtReader.Connect(); err != nil {
		return fmt.Errorf("连接到 SRT 流失败: %w", err)
	}

	// 启动从 SRT 复制数据到多读取器的协程
	go p.processSRTOutput()

	return nil
}

// stopSRT 停止原生 SRT 读取器
func (p *CentralAudioProcessor) stopSRT() {
	if p.srtReader != nil {
		p.logger.Info("正在停止 SRT 读取器")
		_ = p.srtReader.Close()
		p.srtReader = nil
	}

	if p.reconnectTimer != nil {
		p.reconnectTimer.Stop()
		p.reconnectTimer = nil
	}
}

// processSRTOutput 从 SRT 读取并写入多读取器
func (p *CentralAudioProcessor) processSRTOutput() {
	p.logger.Info("开始处理 SRT 输出")

	buffer := make([]byte, 4096)
	bytesProcessed := 0
	lastLogTime := time.Now()
	headerChecked := false

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("上下文已取消,停止 SRT 输出处理",
				Int("total_bytes_processed", bytesProcessed))
			return
		default:
			n, err := p.srtReader.Read(buffer)
			if err != nil {
				if err == io.EOF {
					p.logger.Warn("SRT 流意外结束",
						Int("total_bytes_processed", bytesProcessed),
						String("duration_since_start", time.Since(lastLogTime).String()))
				} else {
					p.logger.Error("从 SRT 读取出错", Error(err),
						Int("total_bytes_processed", bytesProcessed),
						String("duration_since_start", time.Since(lastLogTime).String()))
					p.lastError = err
				}

				// 在延迟后尝试重启 SRT
				p.mu.Lock()
				if p.isRunning && p.reconnectTimer == nil {
					p.logger.Warn("由于读取错误,计划重启 SRT",
						String("error_type", fmt.Sprintf("%T", err)),
						String("error_message", err.Error()))
					// 通知失败状态
					p.notifyStatusChange(StatusFailed, err.Error())
					p.reconnectTimer = time.AfterFunc(p.reconnectDelay, func() {
						p.mu.Lock()
						defer p.mu.Unlock()

						p.reconnectTimer = nil
						if p.isRunning {
							p.logger.Info("正在执行计划的 SRT 重启")
							p.notifyStatusChange(StatusConnecting, "")
							p.stopSRT()
							if err := p.startSRT(); err != nil {
								p.logger.Error("重启 SRT 失败", Error(err))
								p.notifyStatusChange(StatusFailed, err.Error())
							} else {
								p.logger.Info("SRT 重启成功")
								p.notifyStatusChange(StatusConnected, "")
							}
						}
					})
				}
				p.mu.Unlock()
				return
			}

			if n > 0 {
				data := buffer[:n]

				// 在首次读取时,检测并去除存在的 WAV 头。
				// WAV 模式下的 SRT 服务器会将 44 字节的 RIFF 头作为
				// 第一条消息发送。我们将其去除,使所有下游消费者得到原始 PCM 数据。
				if !headerChecked {
					headerChecked = true
					if n >= 4 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' {
						const wavHeaderSize = 44
						if n <= wavHeaderSize {
							p.logger.Info("已从 SRT 流中去除 WAV 头",
								Int("header_bytes", n))
							continue
						}
						p.logger.Info("已从 SRT 流中去除 WAV 头",
							Int("header_bytes", wavHeaderSize))
						data = data[wavHeaderSize:]
					}
				}

				bytesProcessed += len(data)
				p.lastActivity = time.Now()

				// 每 30 秒记录一次进度
				if time.Since(lastLogTime) > 30*time.Second {
					p.logger.Debug("SRT 处理进度",
						Int("bytes_processed", bytesProcessed),
						Int("bytes_this_read", len(data)),
						String("duration", time.Since(lastLogTime).String()))
					lastLogTime = time.Now()
				}

				// 写入多读取器
				if _, err := p.multiReader.Write(data); err != nil {
					p.logger.Error("写入多读取器出错", Error(err),
						Int("bytes_processed_before_error", bytesProcessed))
					return
				}
			}
		}
	}
}

// startFFmpeg 启动用于 HTTP 流的 ffmpeg 进程
func (p *CentralAudioProcessor) startFFmpeg() error {
	p.logger.Debug("正在启动 ffmpeg 进程",
		String("path", p.ffmpegPath),
		String("url", p.audioURL))

	// HTTP 流配置 - 针对低延迟与重连进行优化
	args := []string{
		"-loglevel", "error", // 最少日志
		"-fflags", "nobuffer", // 禁用输入缓冲
		"-flags", "low_delay", // 启用低延迟模式
	}

	// 如已配置,添加超时(将秒转换为微秒)
	if p.ffmpegTimeoutSecs > 0 {
		timeoutMicros := p.ffmpegTimeoutSecs * 1000000
		args = append(args, "-timeout", fmt.Sprintf("%d", timeoutMicros))
	}

	// 添加重连设置
	args = append(args,
		"-reconnect", "1", // 启用重连
		"-reconnect_at_eof", "1", // 在文件末尾时重连
		"-reconnect_streamed", "1", // 对流式输入启用重连
		"-reconnect_delay_max", fmt.Sprintf("%d", p.ffmpegReconnectDelaySecs), // 可配置的重连延迟
		"-i", p.audioURL, // 输入 URL
		"-f", p.format, // 输出格式(应为原始 PCM 的 s16le)
		"-acodec", "pcm_s16le", // 音频编解码器
		"-ac", fmt.Sprintf("%d", p.channels), // 声道数
		"-ar", fmt.Sprintf("%d", p.sampleRate), // 采样率
		"-flush_packets", "1", // 立即刷新数据包
		"pipe:1", // 输出到 stdout
	)

	// 使用增强参数创建 ffmpeg 命令
	p.ffmpegCmd = exec.CommandContext(p.ctx, p.ffmpegPath, args...)

	// 获取 stdout 管道
	var err error
	p.ffmpegStdout, err = p.ffmpegCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout 管道失败: %w", err)
	}

	// 启动 ffmpeg
	if err := p.ffmpegCmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败: %w", err)
	}

	// 启动从 ffmpeg 复制数据到多读取器的协程
	go p.processFFmpegOutput()

	return nil
}

// stopFFmpeg 停止 ffmpeg 进程
func (p *CentralAudioProcessor) stopFFmpeg() {
	if p.ffmpegCmd != nil && p.ffmpegCmd.Process != nil {
		p.logger.Info("正在停止 ffmpeg 进程")

		// 尝试杀死进程,但在关闭过程中不记录错误
		// 这些错误是预期的,因为 ffmpeg 可能已经终止
		_ = p.ffmpegCmd.Process.Kill()

		// 等待进程退出,但不记录错误
		// 退出状态可能是非零的,或进程可能已经消失
		_ = p.ffmpegCmd.Wait()
	}

	if p.reconnectTimer != nil {
		p.reconnectTimer.Stop()
		p.reconnectTimer = nil
	}
}

// processFFmpegOutput 处理来自 ffmpeg 的输出
func (p *CentralAudioProcessor) processFFmpegOutput() {
	p.logger.Info("开始处理 ffmpeg 输出")

	// 创建用于读取的缓冲区
	buffer := make([]byte, 4096)
	bytesProcessed := 0
	lastLogTime := time.Now()

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("上下文已取消,停止 ffmpeg 输出处理",
				Int("total_bytes_processed", bytesProcessed))
			return
		default:
			// 从 ffmpeg 读取
			n, err := p.ffmpegStdout.Read(buffer)
			if err != nil {
				if err == io.EOF {
					p.logger.Warn("FFmpeg 输出意外结束",
						Int("total_bytes_processed", bytesProcessed),
						String("duration_since_start", time.Since(lastLogTime).String()))
				} else {
					p.logger.Error("从 ffmpeg 读取出错", Error(err),
						Int("total_bytes_processed", bytesProcessed),
						String("duration_since_start", time.Since(lastLogTime).String()))
					p.lastError = err
				}

				// 在延迟后尝试重启 ffmpeg
				p.mu.Lock()
				if p.isRunning && p.reconnectTimer == nil {
					p.logger.Warn("由于读取错误,计划重启 ffmpeg",
						String("error_type", fmt.Sprintf("%T", err)),
						String("error_message", err.Error()))
					// 通知失败状态
					p.notifyStatusChange(StatusFailed, err.Error())
					p.reconnectTimer = time.AfterFunc(p.reconnectDelay, func() {
						p.mu.Lock()
						defer p.mu.Unlock()

						p.reconnectTimer = nil
						if p.isRunning {
							p.logger.Info("正在执行计划的 ffmpeg 重启")
							p.notifyStatusChange(StatusConnecting, "")
							p.stopFFmpeg()
							if err := p.startFFmpeg(); err != nil {
								p.logger.Error("重启 ffmpeg 失败", Error(err))
								p.notifyStatusChange(StatusFailed, err.Error())
							} else {
								p.logger.Info("FFmpeg 重启成功")
								p.notifyStatusChange(StatusConnected, "")
							}
						}
					})
				}
				p.mu.Unlock()
				return
			}

			if n > 0 {
				bytesProcessed += n
				// 更新最后活动时间
				p.lastActivity = time.Now()

				// 每 30 秒记录一次进度
				if time.Since(lastLogTime) > 30*time.Second {
					p.logger.Debug("FFmpeg 处理进度",
						Int("bytes_processed", bytesProcessed),
						Int("bytes_this_read", n),
						String("duration", time.Since(lastLogTime).String()))
					lastLogTime = time.Now()
				}

				// 写入多读取器
				if _, err := p.multiReader.Write(buffer[:n]); err != nil {
					p.logger.Error("写入多读取器出错", Error(err),
						Int("bytes_processed_before_error", bytesProcessed))
					return
				}
			}
		}
	}
}

// startMonitoring 启动对音频源(ffmpeg 或 SRT)的监控
func (p *CentralAudioProcessor) startMonitoring() {
	p.monitorTicker = time.NewTicker(5 * time.Second)

	go func() {
		for {
			select {
			case <-p.ctx.Done():
				return
			case <-p.monitorTicker.C:
				p.mu.Lock()
				if p.sourceType == sourceTypeSRT {
					// 监控 SRT 连接
					if p.isRunning && p.srtReader != nil && !p.srtReader.IsConnected() {
						p.logger.Warn("SRT 连接已丢失")

						if p.isRunning && p.reconnectTimer == nil {
							p.logger.Info("连接丢失后正在重启 SRT")
							p.notifyStatusChange(StatusConnecting, "连接丢失后正在重连")
							p.stopSRT()
							if err := p.startSRT(); err != nil {
								p.logger.Error("重启 SRT 失败", Error(err))
								p.notifyStatusChange(StatusFailed, err.Error())
							} else {
								p.notifyStatusChange(StatusConnected, "")
							}
						}
					}
				} else {
					// 监控 ffmpeg 进程
					if p.isRunning && p.ffmpegCmd != nil && p.ffmpegCmd.ProcessState != nil {
						p.logger.Warn("FFmpeg 进程意外退出")

						if p.isRunning && p.reconnectTimer == nil {
							p.logger.Info("进程意外退出后正在重启 ffmpeg")
							p.notifyStatusChange(StatusConnecting, "进程退出后正在重连")
							p.stopFFmpeg()
							if err := p.startFFmpeg(); err != nil {
								p.logger.Error("重启 ffmpeg 失败", Error(err))
								p.notifyStatusChange(StatusFailed, err.Error())
							} else {
								p.notifyStatusChange(StatusConnected, "")
							}
						}
					}
				}
				p.mu.Unlock()
			}
		}
	}()
}

// CreateReader 为音频流创建一个新的读取器(带 WAV 头,用于浏览器播放)
func (p *CentralAudioProcessor) CreateReader(id string) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isRunning {
		var err error
		if p.sourceType == sourceTypeSRT {
			err = p.startSRT()
		} else {
			err = p.startFFmpeg()
		}
		if err != nil {
			return nil, fmt.Errorf("启动处理器失败: %w", err)
		}
		p.isRunning = true
	}

	// 创建带 WAV 头的读取器
	reader := p.multiReader.CreateReader(id)
	return NewWAVReader(reader, p.sampleRate, p.channels), nil
}

// CreateRawReader 为原始 PCM 音频创建一个新的读取器(无 WAV 头,用于转写)
func (p *CentralAudioProcessor) CreateRawReader(id string) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isRunning {
		var err error
		if p.sourceType == sourceTypeSRT {
			err = p.startSRT()
		} else {
			err = p.startFFmpeg()
		}
		if err != nil {
			return nil, fmt.Errorf("启动处理器失败: %w", err)
		}
		p.isRunning = true
	}

	// 返回不带 WAV 头的原始 PCM 读取器
	return p.multiReader.CreateReader(id), nil
}

// RemoveReader 移除一个读取器
func (p *CentralAudioProcessor) RemoveReader(id string) {
	p.multiReader.RemoveReader(id)
}

// GetStatus 返回处理器的状态
func (p *CentralAudioProcessor) GetStatus() (string, time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isRunning {
		return "stopped", p.lastActivity, nil
	}

	if p.lastError != nil {
		return "error", p.lastActivity, p.lastError
	}

	return "running", p.lastActivity, nil
}

// GetContentType 返回音频流的内容类型
func (p *CentralAudioProcessor) GetContentType() string {
	return p.contentType
}

// GetFormat 返回音频流的格式
func (p *CentralAudioProcessor) GetFormat() string {
	return p.format
}
