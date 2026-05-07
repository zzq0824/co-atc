package transcription

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/audio"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// 导入 logger 包的导出函数
var (
	String = logger.String
	Int    = logger.Int
	Int64  = logger.Int64
	Error  = logger.Error
)

// Processor 使用提供的读取器管理特定频率的转写
type Processor struct {
	frequencyID         string
	audioReader         io.ReadCloser
	openaiClient        *OpenAIClient
	wsServer            *websocket.Server
	storage             *sqlite.TranscriptionStorage
	ctx                 context.Context
	cancel              context.CancelFunc
	logger              *logger.Logger
	audioChunker        *audio.AudioChunker
	sessionID           string
	clientSecret        string
	wsConn              *OpenAIWebSocketConn
	wsMu                sync.RWMutex
	chunkCount          int
	chunkCountMu        sync.Mutex
	transcriptionConfig Config
	sessionStartTime    time.Time
	sessionRefreshMu    sync.Mutex
	fileLogger          *FileLogger
}

// NewProcessor 使用提供的读取器创建一个新的转写处理器
func NewProcessor(
	ctx context.Context,
	frequencyID string,
	audioReader io.ReadCloser,
	config Config,
	wsServer *websocket.Server,
	storage *sqlite.TranscriptionStorage,
	logger *logger.Logger,
	fileLogger *FileLogger,
) (ProcessorInterface, error) {
	// 检查是否提供了 OpenAI API 密钥 - 缺失则快速失败
	if config.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("转写处理器需要 OpenAI API 密钥")
	}

	procCtx, procCancel := context.WithCancel(ctx)

	// 创建 OpenAI 客户端
	openaiClient := NewOpenAIClient(config.OpenAIAPIKey, config.Model, config.TimeoutSeconds, logger)

	// 创建处理器
	processor := &Processor{
		frequencyID:         frequencyID,
		audioReader:         audioReader,
		openaiClient:        openaiClient,
		wsServer:            wsServer,
		storage:             storage,
		ctx:                 procCtx,
		cancel:              procCancel,
		logger:              logger.Named("custom-xscribe").With(String("frequency_id", frequencyID)),
		audioChunker:        audio.NewAudioChunker(config.FFmpegSampleRate, config.FFmpegChannels, config.ChunkMs),
		transcriptionConfig: config,
		fileLogger:          fileLogger,
	}

	return processor, nil
}

// Start 启动转写处理器
func (p *Processor) Start() error {
	p.logger.Info("正在启动自定义转写处理器",
		String("frequency_id", p.frequencyID))

	// 创建 OpenAI 转写会话
	var err error
	p.sessionID, p.clientSecret, err = p.openaiClient.CreateSession(p.ctx, p.transcriptionConfig)
	if err != nil {
		p.audioReader.Close()
		return fmt.Errorf("创建转写会话失败: %w", err)
	}
	p.logger.Info("已创建转写会话", String("session_id", p.sessionID))

	// 记录会话启动时间
	p.sessionStartTime = time.Now()

	// 连接到 OpenAI WebSocket
	wsConn, err := p.openaiClient.ConnectWebSocket(p.ctx, p.sessionID, p.clientSecret)
	if err != nil {
		p.audioReader.Close()
		return fmt.Errorf("连接 WebSocket 失败: %w", err)
	}
	p.setWebSocketConn(wsConn)
	p.logger.Info("已连接到 OpenAI WebSocket")

	// 将服务启动事件记录到文件
	if p.fileLogger != nil {
		if err := p.fileLogger.LogServiceStarted(p.frequencyID); err != nil {
			p.logger.Error("写入服务启动日志文件失败", Error(err))
		}
	}

	// 在 goroutine 中启动处理
	go p.processAudio()
	go p.processTranscriptions()
	go p.monitorSessionDuration()

	return nil
}

// Stop 停止转写处理器
func (p *Processor) Stop() error {
	p.logger.Info("正在停止自定义转写处理器")

	// 取消上下文以停止所有操作
	p.cancel()

	// 关闭 WebSocket 连接
	if wsConn := p.getWebSocketConn(); wsConn != nil {
		wsConn.Close()
	}

	// 关闭音频读取器
	if p.audioReader != nil {
		p.audioReader.Close()
	}

	return nil
}

// processAudio 处理来自读取器的音频
func (p *Processor) processAudio() {
	p.logger.Info("正在启动音频处理")

	// 为音频块创建缓冲区
	buffer := make([]byte, 4096)

	// 跟踪连续错误以执行退避
	consecutiveErrors := 0
	maxConsecutiveErrors := 5

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("音频处理因上下文取消而停止")
			return
		default:
			// 从音频源读取
			n, err := p.audioReader.Read(buffer)
			if err != nil {
				if err == io.EOF {
					p.logger.Info("音频源已结束")
					return
				}
				p.logger.Error("从音频源读取错误", Error(err))
				return
			}

			if n > 0 {
				// 处理音频块
				chunks, err := p.audioChunker.ProcessChunk(buffer[:n])
				if err != nil {
					p.logger.Error("处理音频块错误", Error(err))
					continue
				}

				// 将块发送到 OpenAI
				for _, chunk := range chunks {
					// 对块进行 Base64 编码
					encoded := base64.StdEncoding.EncodeToString(chunk)

					// 发送到 OpenAI
					if err := p.sendAudioChunk(encoded); err != nil {
						consecutiveErrors++

						// 根据连续错误数以适当级别记录日志
						if consecutiveErrors <= 2 {
							p.logger.Error("发送音频块错误",
								Error(err),
								Int("consecutive_errors", consecutiveErrors))
						} else if consecutiveErrors == 3 {
							p.logger.Warn("发送音频块出现多次连续错误,即将尝试重新连接",
								Error(err),
								Int("consecutive_errors", consecutiveErrors))
						} else {
							// 在初始错误之后,仅每隔 10 个错误记录一次,以避免日志泛滥
							if consecutiveErrors%10 == 0 {
								p.logger.Warn("音频块发送错误持续出现",
									Error(err),
									Int("consecutive_errors", consecutiveErrors))
							}
						}

						// 在多次连续错误后,持续尝试重新连接。一次失败的重连不应让发送端
						// 永远停留在错误的套接字上。
						if consecutiveErrors >= maxConsecutiveErrors && (consecutiveErrors == maxConsecutiveErrors || consecutiveErrors%10 == 0) {
							p.logger.Info("连续错误过多,正在尝试重新连接 WebSocket")

							// 尝试重新连接
							if err := p.reconnectOpenAI(); err != nil {
								p.logger.Error("重新连接 OpenAI 失败", Error(err))
							} else {
								p.logger.Info("已成功重新连接到 OpenAI")
								consecutiveErrors = 0
							}
						}

						// 添加少量延迟以避免冲击服务
						if consecutiveErrors > 0 {
							// 带上限的指数退避
							backoffMs := 100 * (1 << uint(min(consecutiveErrors-1, 6))) // 上限为 6.4 秒
							time.Sleep(time.Duration(backoffMs) * time.Millisecond)
						}

						continue
					}

					// 发送成功后重置错误计数器
					if consecutiveErrors > 0 {
						p.logger.Info("音频块发送在错误后已恢复",
							Int("previous_consecutive_errors", consecutiveErrors))
						consecutiveErrors = 0
					}
				}
			}
		}
	}
}

// min 返回 x 和 y 中较小的一个
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func (p *Processor) getWebSocketConn() *OpenAIWebSocketConn {
	p.wsMu.RLock()
	defer p.wsMu.RUnlock()
	return p.wsConn
}

func (p *Processor) setWebSocketConn(conn *OpenAIWebSocketConn) {
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	p.wsConn = conn
}

func (p *Processor) isCurrentWebSocketConn(conn *OpenAIWebSocketConn) bool {
	p.wsMu.RLock()
	defer p.wsMu.RUnlock()
	return p.wsConn == conn
}

func (p *Processor) sleepWithContext(duration time.Duration) bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}

// sendAudioChunk 向 OpenAI 发送一个音频块
func (p *Processor) sendAudioChunk(encodedChunk string) error {
	// 创建消息
	message := map[string]interface{}{
		"type":  "input_audio_buffer.append",
		"audio": encodedChunk,
	}

	// 序列化为 JSON
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("序列化音频块消息失败: %w", err)
	}

	// 每 100 个块记录一次日志,避免日志过多
	p.chunkCountMu.Lock()
	p.chunkCount++
	chunkCount := p.chunkCount
	p.chunkCountMu.Unlock()

	if chunkCount%100 == 0 {
		p.logger.Debug("正在发送音频块", Int("chunk_number", chunkCount))
	}

	// 发送到 OpenAI
	wsConn := p.getWebSocketConn()
	if wsConn == nil {
		return fmt.Errorf("WebSocket 连接尚未建立")
	}
	if err := wsConn.Send(string(data)); err != nil {
		return fmt.Errorf("发送音频块失败: %w", err)
	}

	return nil
}

func isReconnectableWebSocketError(errorMsg string) bool {
	reconnectableErrors := []string{
		"websocket: close 1000 (normal)",
		"websocket: close 1001 (going away)",
		"websocket: close 1006 (abnormal closure)",
		"websocket: close 1006 (abnormal closure): unexpected EOF",
		"websocket: close 1011",
		"keepalive ping timeout",
		"use of closed network connection",
		"connection reset by peer",
		"EOF",
		"websocket: close sent",
		"websocket: close received",
		"i/o timeout",
		"read: connection reset by peer",
	}

	for _, reconnectErr := range reconnectableErrors {
		if errorMsg == reconnectErr || strings.Contains(errorMsg, reconnectErr) {
			return true
		}
	}
	return false
}

// processTranscriptions 处理来自 OpenAI 的转写事件
func (p *Processor) processTranscriptions() {
	p.logger.Info("正在启动转写处理",
		String("frequency_id", p.frequencyID),
		String("session_id", p.sessionID))

	// 跟踪重连尝试次数
	reconnectAttempts := 0
	maxReconnectAttempts := 5
	lastReconnectTime := time.Now()
	reconnectBackoffSeconds := 1

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("转写处理因上下文取消而停止")
			return
		default:
			// 接收来自 OpenAI 的消息
			wsConn := p.getWebSocketConn()
			if wsConn == nil {
				p.logger.Warn("OpenAI WebSocket 连接为空,正在尝试重新连接",
					String("frequency_id", p.frequencyID))
				if err := p.reconnectOpenAI(); err != nil {
					p.logger.Error("重新连接空的 OpenAI WebSocket 失败", Error(err))
					if !p.sleepWithContext(time.Duration(reconnectBackoffSeconds) * time.Second) {
						return
					}
					reconnectBackoffSeconds = min(reconnectBackoffSeconds*2, 60)
				}
				continue
			}

			message, err := wsConn.Receive()
			if err != nil {
				// 检查上下文是否已取消或连接是否已关闭
				select {
				case <-p.ctx.Done():
					// 这是关机过程中的预期错误
					p.logger.Info("WebSocket 连接在关机过程中已关闭",
						String("frequency_id", p.frequencyID),
						String("session_id", p.sessionID))
					return
				default:
					// 如果另一个 goroutine 已经替换了此连接,读取错误是关闭过期套接字的
					// 预期结果。继续在当前套接字上接收,而不是开始第二轮重连循环。
					if !p.isCurrentWebSocketConn(wsConn) {
						p.logger.Info("忽略来自过期 OpenAI WebSocket 的读取错误",
							Error(err),
							String("frequency_id", p.frequencyID))
						continue
					}

					// 对错误进行分类
					errorMsg := err.Error()
					isReconnectableError := isReconnectableWebSocketError(errorMsg)

					// 以适当级别记录错误
					if isReconnectableError {
						p.logger.Warn("检测到 WebSocket 连接问题",
							Error(err),
							String("frequency_id", p.frequencyID),
							String("session_id", p.sessionID),
							Int("reconnect_attempts", reconnectAttempts))
					} else {
						p.logger.Error("接收 WebSocket 消息错误",
							Error(err),
							String("frequency_id", p.frequencyID),
							String("session_id", p.sessionID))
					}

					// 关机期间不应在网络错误时立即返回
					if p.ctx.Err() != nil {
						return
					}

					// 对于可重连的错误,尝试以退避方式重新连接
					if isReconnectableError {
						// 检查是否已超出最大重连次数
						if reconnectAttempts >= maxReconnectAttempts {
							timeSinceLastReconnect := time.Since(lastReconnectTime)
							// 如果距离上次重连尝试已经过了一段时间,则重置计数器
							if timeSinceLastReconnect > time.Minute*5 {
								p.logger.Info("冷却期后重置重连计数器",
									String("frequency_id", p.frequencyID))
								reconnectAttempts = 0
								reconnectBackoffSeconds = 1
							} else {
								p.logger.Error("已超过最大重连尝试次数;进入冷却但保持存活",
									String("frequency_id", p.frequencyID),
									Int("max_attempts", maxReconnectAttempts),
									String("cooldown", (time.Minute*5).String()))
								if !p.sleepWithContext(time.Minute * 5) {
									return
								}
								reconnectAttempts = 0
								reconnectBackoffSeconds = 1
								continue
							}
						}

						// 应用指数退避
						backoffDuration := time.Duration(reconnectBackoffSeconds) * time.Second
						p.logger.Info("WebSocket 连接已关闭,等待后再尝试重连",
							String("frequency_id", p.frequencyID),
							String("backoff_duration", backoffDuration.String()),
							Int("attempt", reconnectAttempts+1))

						if !p.sleepWithContext(backoffDuration) {
							return
						}

						// 尝试重新连接
						if err := p.reconnectOpenAI(); err != nil {
							reconnectAttempts++
							reconnectBackoffSeconds = min(reconnectBackoffSeconds*2, 60) // 上限为 60 秒
							p.logger.Error("重新连接 OpenAI 失败",
								Error(err),
								Int("reconnect_attempts", reconnectAttempts),
								Int("next_backoff_seconds", reconnectBackoffSeconds))
						} else {
							p.logger.Info("已成功重新连接到 OpenAI WebSocket",
								String("frequency_id", p.frequencyID),
								String("session_id", p.sessionID))
							reconnectAttempts = 0
							reconnectBackoffSeconds = 1
							lastReconnectTime = time.Now()
						}
						continue
					}

					// 对于其他意外错误,返回
					return
				}
			}

			// 在收到消息后重置重连尝试次数
			if reconnectAttempts > 0 {
				reconnectAttempts = 0
				reconnectBackoffSeconds = 1
			}

			// p.logger.Debug("Received message from OpenAI",
			// 	String("frequency_id", p.frequencyID),
			// 	String("message_length", fmt.Sprintf("%d bytes", len(message))))

			// 解析消息
			var event map[string]interface{}
			if err := json.Unmarshal([]byte(message), &event); err != nil {
				p.logger.Error("解析事件错误", Error(err))
				continue
			}

			// 获取事件类型
			eventType, ok := event["type"].(string)
			if !ok {
				p.logger.Error("事件缺少 type 字段", String("event", message))
				continue
			}

			// 根据类型处理事件
			switch eventType {
			case "conversation.item.input_audio_transcription.delta":
				// 处理增量转写
				deltaText, ok := event["delta"].(string)
				if !ok {
					p.logger.Error("Delta 事件缺少 delta 字段", String("event", message))
					continue
				}

				// 记录增量但不发送给 WebSocket 客户端
				p.logger.Debug("收到增量转写",
					String("frequency_id", p.frequencyID),
					String("text", deltaText))

			case "conversation.item.input_audio_transcription.completed":
				// 处理已完成的转写
				transcript, ok := event["transcript"].(string)
				if !ok {
					p.logger.Error("已完成事件缺少 transcript 字段", String("event", message))
					continue
				}

				// 创建转写事件
				transcriptionEvent := &TranscriptionEvent{
					Type:      "completed",
					Text:      transcript,
					Timestamp: time.Now().UTC(),
				}

				// 处理事件
				if err := p.processTranscriptionEvent(transcriptionEvent); err != nil {
					p.logger.Error("处理已完成转写错误", Error(err))
				}

			case "error":
				// 处理错误
				errorObj, ok := event["error"].(map[string]interface{})
				if !ok {
					p.logger.Error("错误事件缺少 error 字段", String("event", message))
					continue
				}

				errorMessage, ok := errorObj["message"].(string)
				if !ok {
					p.logger.Error("错误对象缺少 message 字段", String("event", message))
					continue
				}

				p.logger.Error("收到来自 OpenAI 的错误", String("error", errorMessage))

				// 检查会话是否已过期
				errorCode, ok := errorObj["code"].(string)
				if ok && errorCode == "session_expired" {
					p.logger.Info("会话已过期,正在重新连接")
					if err := p.reconnectOpenAI(); err != nil {
						p.logger.Error("重新连接 OpenAI 失败", Error(err))
						return
					}
				}
			}
		}
	}
}

// processTranscriptionEvent 处理一个转写事件
func (p *Processor) processTranscriptionEvent(event *TranscriptionEvent) error {
	// 记录事件
	if event.Type == "delta" {
		p.logger.Debug("收到增量转写", String("text", event.Text))
	} else {
		p.logger.Debug("收到已完成的转写", String("text", event.Text))
	}

	// 将已完成的转写存储到数据库
	if event.Type == "completed" {
		// 创建记录
		record := &sqlite.TranscriptionRecord{
			FrequencyID:      p.frequencyID,
			CreatedAt:        event.Timestamp,
			Content:          event.Text,
			IsComplete:       true,
			IsProcessed:      false,
			ContentProcessed: "",
			// SpeakerType 和 Callsign 暂时为空
		}

		// 存储到数据库
		id, err := p.storage.StoreTranscription(record)
		if err != nil {
			return fmt.Errorf("存储转写失败: %w", err)
		}

		p.logger.Debug("已将转写存储到数据库", Int64("id", id))

		// 如果已启用,写入文件日志
		if p.fileLogger != nil {
			if err := p.fileLogger.LogRaw(p.frequencyID, event.Timestamp, event.Text); err != nil {
				p.logger.Error("写入原始转写到日志文件失败", Error(err))
			}
		}

		// 用 ID 更新记录
		record.ID = id

		// 发送到 WebSocket 客户端
		message := &websocket.Message{
			Type: "transcription",
			Data: map[string]interface{}{
				"id":                id,
				"frequency_id":      p.frequencyID,
				"text":              event.Text,
				"timestamp":         event.Timestamp,
				"is_complete":       event.Type == "completed",
				"is_processed":      false,
				"content_processed": "",
			},
		}

		p.logger.Debug("正在向 WebSocket 客户端广播转写",
			String("frequency_id", p.frequencyID),
			String("text", event.Text),
			String("type", event.Type),
			Int64("id", id),
			String("timestamp", event.Timestamp.Format(time.RFC3339)))

		p.wsServer.Broadcast(message)

		return nil
	}

	// 对于增量转写,只发送到 WebSocket 客户端而不存入数据库
	message := &websocket.Message{
		Type: "transcription",
		Data: map[string]interface{}{
			"frequency_id":      p.frequencyID,
			"text":              event.Text,
			"timestamp":         event.Timestamp,
			"is_complete":       event.Type == "completed",
			"is_processed":      false,
			"content_processed": "",
		},
	}

	p.wsServer.Broadcast(message)

	return nil
}

// reconnectOpenAI 重新连接到 OpenAI
func (p *Processor) reconnectOpenAI() error {
	p.sessionRefreshMu.Lock()
	defer p.sessionRefreshMu.Unlock()

	oldConn := p.getWebSocketConn()

	// 创建新会话
	var err error
	p.sessionID, p.clientSecret, err = p.openaiClient.CreateSession(p.ctx, p.transcriptionConfig)
	if err != nil {
		return fmt.Errorf("创建新的转写会话失败: %w", err)
	}
	p.logger.Info("已创建新的转写会话", String("session_id", p.sessionID))

	// 重置会话启动时间
	p.sessionStartTime = time.Now()

	// 在切换到活跃使用之前先连接 WebSocket。这样可以避免在会话创建/拨号路径
	// 重试期间,音频发送端指向已知关闭的连接。
	newConn, err := p.openaiClient.ConnectWebSocket(p.ctx, p.sessionID, p.clientSecret)
	if err != nil {
		return fmt.Errorf("连接 WebSocket 失败: %w", err)
	}
	p.setWebSocketConn(newConn)
	if oldConn != nil && oldConn != newConn {
		oldConn.Close()
	}
	p.logger.Info("已重新连接到 OpenAI WebSocket")

	return nil
}

// monitorSessionDuration 监控会话时长并在过期前刷新
func (p *Processor) monitorSessionDuration() {
	// OpenAI 会话在 30 分钟后过期,因此为安全起见在 25 分钟时刷新
	sessionRefreshInterval := 25 * time.Minute

	for {
		select {
		case <-p.ctx.Done():
			p.logger.Info("会话监控因上下文取消而停止")
			return
		case <-time.After(1 * time.Minute): // 每分钟检查一次
			sessionDuration := time.Since(p.sessionStartTime)

			// 如果会话即将过期,主动刷新
			if sessionDuration >= sessionRefreshInterval {
				p.logger.Info("会话即将过期,正在主动刷新",
					String("frequency_id", p.frequencyID),
					String("session_duration", sessionDuration.String()),
					String("refresh_interval", sessionRefreshInterval.String()))

				if err := p.reconnectOpenAI(); err != nil {
					p.logger.Error("主动刷新会话失败",
						String("frequency_id", p.frequencyID),
						Error(err))
					// 即使刷新失败也继续监控
				} else {
					p.logger.Info("已在过期前成功刷新会话",
						String("frequency_id", p.frequencyID))
				}
			}
		}
	}
}
