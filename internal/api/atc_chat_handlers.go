package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/yegors/co-atc/internal/atcchat"
	"github.com/yegors/co-atc/pkg/logger"
)

// SafeWebSocketConn 使用互斥锁包装 WebSocket 连接以保证写入的线程安全
type SafeWebSocketConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// WriteMessage 安全地向 WebSocket 连接写入消息
func (s *SafeWebSocketConn) WriteMessage(messageType int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(messageType, data)
}

// ReadMessage 从 WebSocket 连接读取消息(读取无需互斥锁)
func (s *SafeWebSocketConn) ReadMessage() (int, []byte, error) {
	return s.conn.ReadMessage()
}

// Close 关闭 WebSocket 连接
func (s *SafeWebSocketConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.Close()
}

// NewSafeWebSocketConn 创建一个新的安全 WebSocket 连接包装器
func NewSafeWebSocketConn(conn *websocket.Conn) *SafeWebSocketConn {
	return &SafeWebSocketConn{
		conn: conn,
	}
}

// ATCChatHandlers 包含 ATC 聊天功能的处理器
type ATCChatHandlers struct {
	service  *atcchat.Service
	logger   *logger.Logger
	upgrader websocket.Upgrader
}

// NewATCChatHandlers 创建新的 ATC 聊天处理器
func NewATCChatHandlers(service *atcchat.Service, logger *logger.Logger) *ATCChatHandlers {
	return &ATCChatHandlers{
		service: service,
		logger:  logger.Named("atc-chat-handlers"),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// 暂时允许所有来源 - 在生产环境中应限制此项
				return true
			},
		},
	}
}

// CreateSession 创建一个新的 ATC 聊天会话
func (h *ATCChatHandlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	h.logger.Info("正在创建新的 ATC 聊天会话")

	session, err := h.service.CreateSession(r.Context())
	if err != nil {
		h.logger.Error("创建会话失败", logger.Error(err))
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(session); err != nil {
		h.logger.Error("编码会话响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}

	h.logger.Info("成功创建会话",
		logger.String("session_id", session.ID))
}

// GetSession 检索现有的 ATC 聊天会话
func (h *ATCChatHandlers) GetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	session, err := h.service.GetSession(sessionID)
	if err != nil {
		h.logger.Error("获取会话失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(session); err != nil {
		h.logger.Error("编码会话响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// EndSession 终止 ATC 聊天会话
func (h *ATCChatHandlers) EndSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	if err := h.service.EndSession(r.Context(), sessionID); err != nil {
		h.logger.Error("结束会话失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, "Failed to end session", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	h.logger.Info("成功结束会话",
		logger.String("session_id", sessionID))
}

// UpdateSessionContext 使用最新的空域数据更新会话上下文
// 当用户开始说话(按下通话)时调用此函数
func (h *ATCChatHandlers) UpdateSessionContext(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	h.logger.Debug("收到更新会话上下文的请求",
		logger.String("session_id", sessionID))

	// 使用最新的空域数据更新会话上下文
	if err := h.service.UpdateSessionContextOnDemand(sessionID); err != nil {
		h.logger.Error("更新会话上下文失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, fmt.Sprintf("Failed to update session context: %v", err), http.StatusInternalServerError)
		return
	}

	h.logger.Info("会话上下文更新成功",
		logger.String("session_id", sessionID))

	// 返回成功响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Session context updated with fresh airspace data",
	})
}

// WebSocketMessage 表示通过 WebSocket 连接发送的消息
type WebSocketMessage struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// AudioData 表示 WebSocket 消息中的音频数据
type AudioData struct {
	Format    string `json:"format"`
	Data      string `json:"data"` // Base64 编码的音频数据
	Timestamp int64  `json:"timestamp"`
}

// WebSocketHandler 处理实时音频的 WebSocket 连接
func (h *ATCChatHandlers) WebSocketHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	// 验证会话存在并且有效
	session, err := h.service.GetSession(sessionID)
	if err != nil {
		h.logger.Error("WebSocket 连接未找到会话",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	// 将连接升级为 WebSocket
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("升级到 WebSocket 失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		return
	}
	defer conn.Close()

	h.logger.Info("WebSocket 连接已建立",
		logger.String("session_id", sessionID))

	// 为此连接创建上下文
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 启动实时音频桥接
	if err := h.bridgeRealtimeAudio(ctx, conn, session); err != nil {
		// 仅记录意外的 WebSocket 错误,不记录正常关闭
		if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
			h.logger.Error("实时音频桥接失败",
				logger.String("session_id", sessionID),
				logger.Error(err))
		} else {
			h.logger.Debug("实时音频桥接正常结束",
				logger.String("session_id", sessionID),
				logger.Error(err))
		}
	}

	h.logger.Info("WebSocket 连接已关闭",
		logger.String("session_id", sessionID))
}

// bridgeRealtimeAudio 处理客户端与 OpenAI 之间的双向音频流
func (h *ATCChatHandlers) bridgeRealtimeAudio(ctx context.Context, clientConn *websocket.Conn, session *atcchat.ChatSession) error {
	h.logger.Info("正在启动实时音频桥接",
		logger.String("session_id", session.ID))

	// 注册此 WebSocket 连接以接收上下文更新
	updateChan := h.service.RegisterWebSocketConnection(session.ID)
	defer h.service.UnregisterWebSocketConnection(session.ID)

	// 向客户端发送就绪消息以确认连接已建立
	readyMsg := map[string]interface{}{
		"type":       "connection_ready",
		"session_id": session.ID,
	}
	if err := clientConn.WriteJSON(readyMsg); err != nil {
		return fmt.Errorf("向客户端发送就绪消息失败: %w", err)
	}

	// 初始连接使用静态提示词 - 模板化数据将通过 session.update 发送
	systemPrompt := "You are an experienced Air Traffic Controller assistant. Real-time airspace data will be provided via system updates."

	// 从服务获取已配置的模型
	model := h.service.GetRealtimeModel()
	if model == "" {
		model = "gpt-4o-realtime-preview-2024-12-17" // 回退
	}

	h.logger.Debug("使用模型和提示词",
		logger.String("session_id", session.ID),
		logger.String("model", model),
		logger.String("prompt_length", fmt.Sprintf("%d chars", len(systemPrompt))))

	// 连接到 OpenAI 实时 WebSocket(可能需要时间)
	h.logger.Info("正在连接到 OpenAI 实时 API",
		logger.String("session_id", session.ID))

	rawOpenaiConn, err := h.connectToOpenAI(ctx, session, systemPrompt, model)
	if err != nil {
		// 向客户端发送错误消息
		errorMsg := map[string]interface{}{
			"type":  "connection_error",
			"error": "Failed to connect to OpenAI API",
		}
		clientConn.WriteJSON(errorMsg)
		return fmt.Errorf("连接到 OpenAI 失败: %w", err)
	}
	defer rawOpenaiConn.Close()

	// 向客户端发送 OpenAI 连接就绪消息
	openaiReadyMsg := map[string]interface{}{
		"type":       "openai_ready",
		"session_id": session.ID,
	}
	if err := clientConn.WriteJSON(openaiReadyMsg); err != nil {
		return fmt.Errorf("向客户端发送 OpenAI 就绪消息失败: %w", err)
	}

	// 使用安全的 WebSocket 连接进行包装以保证写入的线程安全
	openaiConn := NewSafeWebSocketConn(rawOpenaiConn)

	// 启动双向消息转发
	errChan := make(chan error, 3)

	// 将消息从客户端转发到 OpenAI
	go func() {
		errChan <- h.forwardClientToOpenAI(ctx, clientConn, openaiConn, session)
	}()

	// 将消息从 OpenAI 转发到客户端
	go func() {
		errChan <- h.forwardOpenAIToClient(ctx, openaiConn, clientConn, session, systemPrompt)
	}()

	// 处理来自服务的上下文更新
	go func() {
		errChan <- h.handleContextUpdates(ctx, openaiConn, session, updateChan)
	}()

	// 等待任意 goroutine 完成或出错
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetSessionStatus 返回会话的当前状态
func (h *ATCChatHandlers) GetSessionStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	status, err := h.service.GetSessionStatus(sessionID)
	if err != nil {
		h.logger.Error("获取会话状态失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, "Failed to get session status", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		h.logger.Error("编码状态响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// connectToOpenAI 建立到 OpenAI 实时 API 的 WebSocket 连接
func (h *ATCChatHandlers) connectToOpenAI(ctx context.Context, session *atcchat.ChatSession, systemPrompt, model string) (*websocket.Conn, error) {
	// 从配置获取 OpenAI API 密钥
	config := h.service.GetConfig()
	if config.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("OpenAI API 密钥未配置")
	}

	var url string
	var headers http.Header

	// 检查我们是否有通过 REST API 创建的 OpenAI 会话
	if session.OpenAISessionID != "" && session.ClientSecret != "" {
		// 使用基于会话的带模型参数的 WebSocket 连接
		url = fmt.Sprintf("wss://api.openai.com/v1/realtime?model=%s", model)
		headers = http.Header{}
		headers.Set("Authorization", "Bearer "+session.ClientSecret)
		headers.Set("OpenAI-Beta", "realtime=v1")

		h.logger.Info("使用会话凭证连接到 OpenAI 实时 API",
			logger.String("session_id", session.ID),
			logger.String("openai_session_id", session.OpenAISessionID),
			logger.String("url", url),
			logger.String("auth_type", "session_client_secret"))
	} else {
		// 回退到直接 WebSocket 连接
		url = "wss://api.openai.com/v1/realtime?model=" + model
		headers = http.Header{}
		headers.Set("Authorization", "Bearer "+config.OpenAIAPIKey)
		headers.Set("OpenAI-Beta", "realtime=v1")

		h.logger.Warn("未找到 OpenAI 会话,使用直接 WebSocket 连接",
			logger.String("session_id", session.ID),
			logger.String("model", model),
			logger.String("url", url),
			logger.String("auth_type", "api_key"))
	}

	h.logger.Debug("正在连接到 OpenAI 实时 API",
		logger.String("session_id", session.ID),
		logger.String("url", url))

	// 连接到 OpenAI
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	conn, resp, err := dialer.DialContext(ctx, url, headers)
	if err != nil {
		// 记录详细的错误信息
		if resp != nil {
			h.logger.Error("WebSocket 握手失败,带 HTTP 响应",
				logger.String("session_id", session.ID),
				logger.String("url", url),
				logger.Int("status_code", resp.StatusCode),
				logger.String("status", resp.Status))

			// 尝试读取响应正文以获取更多详细信息
			if resp.Body != nil {
				bodyBytes, readErr := io.ReadAll(resp.Body)
				if readErr == nil {
					h.logger.Error("WebSocket 握手错误响应正文",
						logger.String("session_id", session.ID),
						logger.String("response_body", string(bodyBytes)))
				}
			}
		}
		return nil, fmt.Errorf("连接到 OpenAI 失败: %w", err)
	}

	h.logger.Info("已连接到 OpenAI 实时 API",
		logger.String("session_id", session.ID),
		logger.String("openai_session_id", session.OpenAISessionID))

	return conn, nil
}

// sendSessionUpdate 发送带有系统提示词的 session.update 事件
func (h *ATCChatHandlers) sendSessionUpdate(conn *SafeWebSocketConn, session *atcchat.ChatSession, systemPrompt string) error {
	// 获取会话参数的配置
	config := h.service.GetConfig()

	sessionData := map[string]interface{}{
		"modalities":                 []string{"text", "audio"},
		"instructions":               systemPrompt,
		"voice":                      config.Voice,
		"input_audio_format":         config.InputAudioFormat,
		"output_audio_format":        config.OutputAudioFormat,
		"temperature":                config.Temperature,
		"max_response_output_tokens": config.MaxResponseTokens,
		"tool_choice":                "auto",
		"tools":                      []interface{}{},
	}

	// 仅在未禁用时添加轮次检测
	if config.TurnDetectionType != "" && config.TurnDetectionType != "none" {
		sessionData["turn_detection"] = map[string]interface{}{
			"type":                config.TurnDetectionType,
			"threshold":           config.VADThreshold,
			"prefix_padding_ms":   300,
			"silence_duration_ms": config.SilenceDurationMs,
			"create_response":     true,
			"interrupt_response":  true,
		}
	} else {
		// 显式设置为 null 以禁用轮次检测
		sessionData["turn_detection"] = nil
	}

	sessionUpdate := map[string]interface{}{
		"type":    "session.update",
		"session": sessionData,
	}

	// 添加输入音频转写
	sessionData["input_audio_transcription"] = map[string]interface{}{
		"model": "whisper-1",
	}

	updateData, err := json.Marshal(sessionUpdate)
	if err != nil {
		return fmt.Errorf("序列化会话更新失败: %w", err)
	}

	h.logger.Info("正在发送带自定义指令的 session.update",
		logger.String("session_id", session.ID),
		logger.Int("prompt_length", len(systemPrompt)),
		logger.String("prompt_preview", systemPrompt[:min(200, len(systemPrompt))]),
		logger.String("voice", config.Voice),
		logger.Float64("temperature", config.Temperature))

	// 记录完整的会话更新以便调试
	h.logger.Debug("完整的 session.update 负载",
		logger.String("session_id", session.ID),
		logger.String("payload", string(updateData)))

	if err := conn.WriteMessage(websocket.TextMessage, updateData); err != nil {
		return fmt.Errorf("发送会话更新失败: %w", err)
	}

	h.logger.Info("成功向 OpenAI 发送 session.update",
		logger.String("session_id", session.ID))

	return nil
}

// handleContextUpdates 监听来自服务的上下文更新并向 OpenAI 发送 session.update 事件
func (h *ATCChatHandlers) handleContextUpdates(ctx context.Context, openaiConn *SafeWebSocketConn, session *atcchat.ChatSession, updateChan <-chan string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case updateMessage, ok := <-updateChan:
			if !ok {
				h.logger.Info("上下文更新通道已关闭", logger.String("session_id", session.ID))
				return nil
			}

			h.logger.Debug("收到上下文更新,正在转发到 OpenAI",
				logger.String("session_id", session.ID),
				logger.Int("message_length", len(updateMessage)))

			// 服务发送一个完整的 JSON 消息,因此我们需要解析它并作为原始消息发送
			if err := openaiConn.WriteMessage(websocket.TextMessage, []byte(updateMessage)); err != nil {
				h.logger.Error("向 OpenAI 发送上下文更新失败",
					logger.String("session_id", session.ID),
					logger.Error(err))
				return fmt.Errorf("发送上下文更新失败: %w", err)
			}

			h.logger.Debug("成功向 OpenAI 发送上下文更新",
				logger.String("session_id", session.ID))
		}
	}
}

// forwardClientToOpenAI 将消息从客户端转发到 OpenAI
func (h *ATCChatHandlers) forwardClientToOpenAI(ctx context.Context, clientConn *websocket.Conn, openaiConn *SafeWebSocketConn, session *atcchat.ChatSession) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			messageType, message, err := clientConn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					h.logger.Error("客户端 WebSocket 错误", logger.Error(err))
				}
				return err
			}

			h.logger.Debug("正在向 OpenAI 转发客户端消息",
				logger.String("session_id", session.ID),
				logger.Int("message_type", messageType),
				logger.Int("size", len(message)))

			// 将消息转发到 OpenAI
			if err := openaiConn.WriteMessage(messageType, message); err != nil {
				h.logger.Error("向 OpenAI 转发消息失败", logger.Error(err))
				return err
			}
		}
	}
}

// forwardOpenAIToClient 将消息从 OpenAI 转发到客户端
func (h *ATCChatHandlers) forwardOpenAIToClient(ctx context.Context, openaiConn *SafeWebSocketConn, clientConn *websocket.Conn, session *atcchat.ChatSession, systemPrompt string) error {
	sessionUpdateSent := false
	usingSessionBasedConnection := session.OpenAISessionID != "" && session.ClientSecret != ""

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			messageType, message, err := openaiConn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					h.logger.Error("OpenAI WebSocket 错误", logger.Error(err))
				}
				return err
			}

			// 解析并记录重要事件
			if messageType == websocket.TextMessage {
				var event map[string]interface{}
				if err := json.Unmarshal(message, &event); err == nil {
					if eventType, ok := event["type"].(string); ok {
						switch eventType {
						case "session.created":
							h.logger.Info("从 OpenAI 收到 session.created",
								logger.String("session_id", session.ID),
								logger.Bool("using_session_based_connection", usingSessionBasedConnection))

							// 始终在 session.created 之后发送 session.update,无论连接类型如何
							// REST API 会话创建实际上不会应用指令 - 它们必须通过 WebSocket 设置
							if !sessionUpdateSent {
								h.logger.Info("正在发送 session.update 以应用自定义指令",
									logger.String("session_id", session.ID),
									logger.Bool("using_session_based_connection", usingSessionBasedConnection))

								// 为初始更新生成最新的模板化提示词
								templatedPrompt, err := h.service.GenerateSystemPrompt(session.ID)
								if err != nil {
									h.logger.Error("为初始会话更新生成模板化提示词失败", logger.Error(err))
									// 回退到静态提示词
									templatedPrompt = systemPrompt
								}

								if err := h.sendSessionUpdate(openaiConn, session, templatedPrompt); err != nil {
									h.logger.Error("session.created 之后发送会话更新失败", logger.Error(err))
									return err
								}
								sessionUpdateSent = true
							}

							// 记录 session.created 事件中的指令以便比较
							if sessionData, ok := event["session"].(map[string]interface{}); ok {
								if instructions, ok := sessionData["instructions"].(string); ok {
									h.logger.Info("session.created 中的会话指令(更新前)",
										logger.String("session_id", session.ID),
										logger.Int("instructions_length", len(instructions)),
										logger.String("instructions_preview", instructions[:min(200, len(instructions))]))

								}
							}

						case "session.updated":
							h.logger.Info("从 OpenAI 收到 session.updated - 指令已应用!",
								logger.String("session_id", session.ID))

							// 记录已应用的实际指令
							if sessionData, ok := event["session"].(map[string]interface{}); ok {
								if instructions, ok := sessionData["instructions"].(string); ok {
									h.logger.Info("已确认 session.updated 中的指令",
										logger.String("session_id", session.ID),
										logger.Int("instructions_length", len(instructions)),
										logger.String("instructions_preview", instructions[:min(200, len(instructions))]))
								}
							}

						case "error":
							h.logger.Error("从 OpenAI 收到错误",
								logger.String("session_id", session.ID),
								logger.Any("error", event))
						}
					}
				}
			}

			h.logger.Debug("正在向客户端转发 OpenAI 消息",
				logger.String("session_id", session.ID),
				logger.Int("message_type", messageType),
				logger.Int("size", len(message)))

			// 将消息转发到客户端
			if err := clientConn.WriteMessage(messageType, message); err != nil {
				h.logger.Error("向客户端转发消息失败", logger.Error(err))
				return err
			}
		}
	}
}
