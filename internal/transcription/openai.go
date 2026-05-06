package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// OpenAIClient 处理与 OpenAI 的实时转写 API 的通信
type OpenAIClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *logger.Logger
}

// OpenAIWebSocketConn 表示到 OpenAI 的 WebSocket 连接
type OpenAIWebSocketConn struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	closed    bool
	closeChan chan struct{}
}

// NewOpenAIClient 创建一个新的 OpenAI 客户端
func NewOpenAIClient(apiKey, model string, timeoutSeconds int, logger *logger.Logger) *OpenAIClient {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second // 如果未指定,默认为 2 分钟
	}

	if apiKey == "" {
		logger.Warn("OpenAI API 密钥为空 - 转写和后处理功能将无法工作")
	}

	return &OpenAIClient{
		apiKey: apiKey,
		model:  model,
		logger: logger.Named("openai"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// CreateSession 创建一个新的转写会话
func (c *OpenAIClient) CreateSession(ctx context.Context, config Config) (string, string, error) {
	// 检查是否提供 OpenAI API 密钥 - 如果缺失则快速失败
	if c.apiKey == "" {
		return "", "", fmt.Errorf("转写会话需要 OpenAI API 密钥")
	}

	c.logger.Info("正在创建新的 OpenAI 转写会话",
		logger.String("model", c.model),
		logger.String("language", config.Language),
		logger.String("noise_reduction", config.NoiseReduction))
	// 使用扁平字段构建请求体(创建端点使用此格式)
	type InputAudioNoiseReduction struct {
		Type string `json:"type"`
	}

	type InputAudioTranscription struct {
		Model    string `json:"model"`
		Language string `json:"language,omitempty"`
		Prompt   string `json:"prompt,omitempty"`
	}

	type TurnDetection struct {
		Type              string   `json:"type,omitempty"`
		PrefixPaddingMs   *int     `json:"prefix_padding_ms,omitempty"`
		SilenceDurationMs *int     `json:"silence_duration_ms,omitempty"`
		Threshold         *float64 `json:"threshold,omitempty"`
	}

	type TranscriptionSessionRequest struct {
		InputAudioFormat         string                    `json:"input_audio_format"`
		InputAudioTranscription  *InputAudioTranscription  `json:"input_audio_transcription"`
		InputAudioNoiseReduction *InputAudioNoiseReduction `json:"input_audio_noise_reduction,omitempty"`
		TurnDetection            *TurnDetection            `json:"turn_detection,omitempty"`
	}

	// 创建请求体
	reqBody := TranscriptionSessionRequest{
		InputAudioFormat: "pcm16",
		InputAudioTranscription: &InputAudioTranscription{
			Model:    c.model,
			Language: config.Language,
			Prompt:   config.Prompt,
		},
	}

	// 如果指定,添加噪声降低(对 "none" 或空值省略 — API 需要 null 来禁用)
	if config.NoiseReduction != "" && config.NoiseReduction != "none" {
		reqBody.InputAudioNoiseReduction = &InputAudioNoiseReduction{
			Type: config.NoiseReduction,
		}
	}

	// 如果指定,添加轮次检测
	if config.TurnDetectionType != "" {
		prefixPaddingMs := config.PrefixPaddingMs
		silenceDurationMs := config.SilenceDurationMs
		threshold := config.VADThreshold

		reqBody.TurnDetection = &TurnDetection{
			Type: config.TurnDetectionType,
		}

		// 仅添加非零值
		if prefixPaddingMs > 0 {
			reqBody.TurnDetection.PrefixPaddingMs = &prefixPaddingMs
		}

		if silenceDurationMs > 0 {
			reqBody.TurnDetection.SilenceDurationMs = &silenceDurationMs
		}

		if threshold > 0 {
			reqBody.TurnDetection.Threshold = &threshold
		}
	}

	// 将请求体序列化为 JSON
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", fmt.Errorf("序列化请求体失败: %w", err)
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/realtime/transcription_sessions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", "", fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置头部
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("openai-beta", "realtime=v1")

	// 执行请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("执行请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("意外的状态码: %d,响应: %s", resp.StatusCode, string(bodyBytes))
	}

	// 读取响应体用于日志记录
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应体失败: %w", err)
	}

	// 记录响应体
	c.logger.Debug("OpenAI API 响应",
		logger.String("response", string(bodyBytes)))

	sessionID, clientSecret, err := parseTranscriptionSessionResponse(bodyBytes)
	if err != nil {
		return "", "", err
	}

	// 记录解析结果
	secretPrefix := clientSecret
	if len(secretPrefix) > 10 {
		secretPrefix = secretPrefix[:10] + "..."
	}
	c.logger.Debug("已解析 OpenAI API 响应",
		logger.String("session_id", sessionID),
		logger.String("client_secret_value_prefix", secretPrefix))

	return sessionID, clientSecret, nil
}

func parseTranscriptionSessionResponse(bodyBytes []byte) (string, string, error) {
	var result struct {
		SessionID    string `json:"id"`
		ClientSecret struct {
			Value     string `json:"value"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"client_secret"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %w", err)
	}

	if result.SessionID == "" {
		return "", "", fmt.Errorf("解析响应失败: 缺少 session id")
	}
	if result.ClientSecret.Value == "" {
		return "", "", fmt.Errorf("解析响应失败: 缺少 client secret")
	}

	return result.SessionID, result.ClientSecret.Value, nil
}

func realtimeTranscriptionWebSocketURL() string {
	return "wss://api.openai.com/v1/realtime?intent=transcription"
}

// ConnectWebSocket 建立到转写 API 的 WebSocket 连接,带重连逻辑
func (c *OpenAIClient) ConnectWebSocket(ctx context.Context, sessionID, clientSecret string) (*OpenAIWebSocketConn, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("连接转写 WebSocket 需要 session id")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("连接转写 WebSocket 需要 client secret")
	}

	// 创建 WebSocket URL
	wsURL := realtimeTranscriptionWebSocketURL()
	c.logger.Debug("正在连接 OpenAI WebSocket",
		logger.String("url", wsURL),
		logger.String("session_id", sessionID))

	// 创建 WebSocket 拨号器
	dialer := websocket.Dialer{
		HandshakeTimeout: 45 * time.Second,
	}

	// 设置头部
	headers := http.Header{}
	headers.Set("Authorization", fmt.Sprintf("Bearer %s", clientSecret))
	headers.Set("openai-beta", "realtime=v1")

	// 使用重试逻辑连接到 WebSocket
	var conn *websocket.Conn
	var resp *http.Response
	var err error

	maxRetries := 3
	retryInterval := 2 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		c.logger.Debug("正在尝试连接 OpenAI WebSocket",
			logger.Int("attempt", attempt+1),
			logger.Int("max_attempts", maxRetries))

		conn, resp, err = dialer.DialContext(ctx, wsURL, headers)
		if err == nil {
			c.logger.Debug("成功连接到 OpenAI WebSocket",
				logger.String("status", resp.Status))
			break
		}

		c.logger.Error("连接 OpenAI WebSocket 失败",
			logger.Int("attempt", attempt+1),
			logger.Error(err))

		if attempt == maxRetries-1 {
			return nil, fmt.Errorf("尝试 %d 次后连接 WebSocket 失败: %w", maxRetries, err)
		}

		// 重试前等待
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryInterval):
			// 继续重试
		}
	}

	// 创建 WebSocket 连接
	wsConn := &OpenAIWebSocketConn{
		conn:      conn,
		closeChan: make(chan struct{}),
	}

	return wsConn, nil
}

const (
	openAIWebSocketWriteTimeout = 30 * time.Second
	openAIWebSocketReadTimeout  = 10 * time.Minute
)

// Send 向 WebSocket 发送消息
func (ws *OpenAIWebSocketConn) Send(message string) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.closed {
		return fmt.Errorf("WebSocket 连接已关闭")
	}

	if err := ws.conn.SetWriteDeadline(time.Now().Add(openAIWebSocketWriteTimeout)); err != nil {
		return fmt.Errorf("设置 WebSocket 写入截止时间失败: %w", err)
	}
	return ws.conn.WriteMessage(websocket.TextMessage, []byte(message))
}

// Receive 从 WebSocket 接收消息
func (ws *OpenAIWebSocketConn) Receive() (string, error) {
	if err := ws.conn.SetReadDeadline(time.Now().Add(openAIWebSocketReadTimeout)); err != nil {
		return "", fmt.Errorf("设置 WebSocket 读取截止时间失败: %w", err)
	}
	_, message, err := ws.conn.ReadMessage()
	if err != nil {
		return "", err
	}

	return string(message), nil
}

// Close 关闭 WebSocket 连接
func (ws *OpenAIWebSocketConn) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.closed {
		return nil
	}

	ws.closed = true
	close(ws.closeChan)
	return ws.conn.Close()
}

// PostProcessTranscription 将转写发送到 OpenAI 进行后处理
func (c *OpenAIClient) PostProcessTranscription(ctx context.Context, content string, systemPrompt string, model string) (*PostProcessingResult, error) {
	// 检查是否提供 OpenAI API 密钥 - 如果缺失则快速失败
	if c.apiKey == "" {
		return nil, fmt.Errorf("后处理需要 OpenAI API 密钥")
	}

	c.logger.Debug("正在对转写进行后处理",
		logger.String("content", content),
		logger.String("model", model))

	// 创建请求体
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	type Request struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		MaxTokens   int       `json:"max_tokens,omitempty"`
		Temperature float64   `json:"temperature"`
	}

	// 创建消息
	messages := []Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: content,
		},
	}

	// 创建请求
	request := Request{
		Model:       model,
		Messages:    messages,
		MaxTokens:   2048, // 根据需要调整
		Temperature: 0.0,  // 设置温度为 0 以获得确定性输出
	}

	// 将请求序列化为 JSON
	jsonData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置头部
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	// 执行请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("执行请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("意外的状态码: %d,响应: %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	// 读取响应体
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	// 解析响应
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	// 检查我们是否有 choices
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("响应中没有 choices")
	}

	// 将内容解析为 JSON
	var processingResult PostProcessingResult
	if err := json.Unmarshal([]byte(result.Choices[0].Message.Content), &processingResult); err != nil {
		return nil, fmt.Errorf("解析处理结果失败: %w", err)
	}

	return &processingResult, nil
}

// PostProcessBatch 将一批转写发送到 OpenAI 进行后处理
func (c *OpenAIClient) PostProcessBatch(ctx context.Context, systemPrompt string, userInput string, model string) ([]TranscriptionBatch, error) {
	// 检查是否提供 OpenAI API 密钥 - 如果缺失则快速失败
	if c.apiKey == "" {
		return nil, fmt.Errorf("后处理需要 OpenAI API 密钥")
	}

	c.logger.Debug("正在对批量转写进行后处理",
		logger.String("model", model),
		logger.Int("system_prompt_length", len(systemPrompt)),
		logger.Int("user_input_length", len(userInput)))

	// 创建请求体
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	type Request struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		MaxTokens   int       `json:"max_tokens,omitempty"`
		Temperature float64   `json:"temperature"`
	}

	// 创建消息
	messages := []Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: userInput,
		},
	}

	// 创建请求
	request := Request{
		Model:       model,
		Messages:    messages,
		MaxTokens:   4096, // 为批处理增加
		Temperature: 0.0,  // 设置温度为 0 以获得确定性输出
	}

	// 将请求序列化为 JSON
	jsonData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 在 info 级别记录完整请求用于审计
	prettyRequest, _ := json.MarshalIndent(request, "", "  ")
	c.logger.Debug("OpenAI 后处理请求",
		logger.String("model", model),
		logger.Int("system_prompt_length", len(systemPrompt)),
		logger.Int("user_input_length", len(userInput)),
		logger.String("system_prompt", systemPrompt),
		logger.String("user_input", userInput),
		logger.String("full_request", string(prettyRequest)))

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置头部
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	// 执行请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("执行请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("意外的状态码: %d,响应: %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	// 读取响应体
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	// 在 info 级别记录响应用于审计
	c.logger.Info("OpenAI 后处理响应",
		logger.Int("status_code", resp.StatusCode),
		logger.Int("response_length", len(bodyBytes)),
		logger.String("response", string(bodyBytes)))

	// 解析响应
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	// 检查我们是否有 choices
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("响应中没有 choices")
	}

	// 从响应中提取内容
	content := result.Choices[0].Message.Content

	// 在内容中查找 JSON 数组(以防有附加文本)
	startIdx := strings.Index(content, "[")
	endIdx := strings.LastIndex(content, "]")

	if startIdx == -1 || endIdx == -1 || startIdx >= endIdx {
		// 记录错误以及完整响应内容用于调试
		c.logger.Error("在 OpenAI 响应中找不到 JSON 数组 - 这表示 LLM 未遵循预期格式",
			logger.String("full_response", content),
			logger.String("model", model))
		return nil, fmt.Errorf("OpenAI 响应不包含有效的 JSON 数组: %s", content)
	}

	jsonContent := content[startIdx : endIdx+1]

	// 将内容解析为 TranscriptionBatch 的 JSON 数组
	var results []TranscriptionBatch
	if err := json.Unmarshal([]byte(jsonContent), &results); err != nil {
		// 记录错误以及提取的 JSON 内容用于调试
		c.logger.Error("将 OpenAI 响应解码为 JSON 数组失败",
			logger.String("error", err.Error()),
			logger.String("extracted_json", jsonContent),
			logger.String("full_response", content),
			logger.String("model", model))
		return nil, fmt.Errorf("将 OpenAI 响应解析为 JSON 失败: %w", err)
	}

	// 记录成功解析以及结果数量
	c.logger.Debug("成功解析 OpenAI 响应",
		logger.Int("result_count", len(results)),
		logger.String("model", model))

	return results, nil
}
