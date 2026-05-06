package atcchat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// RealtimeClient 处理 OpenAI Realtime API 交互
// 注意:这是一个简化的实现,因为 OpenAI Go SDK 尚不支持 Realtime API
type RealtimeClient struct {
	apiKey     string
	httpClient *http.Client
	config     SessionConfig
	logger     *logger.Logger
}

// NewRealtimeClient 创建一个新的 OpenAI Realtime 客户端
func NewRealtimeClient(apiKey string, config SessionConfig, logger *logger.Logger) *RealtimeClient {
	if apiKey == "" {
		logger.Warn("OpenAI API 密钥为空 - ATC Chat 功能将无法工作")
	}

	return &RealtimeClient{
		apiKey: apiKey,
		config: config,
		logger: logger.Named("realtime-client"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SessionRequest 表示创建 Realtime 会话的请求
type SessionRequest struct {
	InputAudioFormat         string                `json:"input_audio_format"`
	OutputAudioFormat        string                `json:"output_audio_format"`
	Instructions             string                `json:"instructions"`
	MaxResponseTokens        int                   `json:"max_response_output_tokens"`
	Modalities               []string              `json:"modalities"`
	Model                    string                `json:"model"`
	Temperature              float64               `json:"temperature"`
	Voice                    string                `json:"voice"`
	TurnDetection            *TurnDetectionConfig  `json:"turn_detection,omitempty"`
	InputAudioNoiseReduction *NoiseReductionConfig `json:"input_audio_noise_reduction,omitempty"`
}

// TurnDetectionConfig 表示轮次检测配置
type TurnDetectionConfig struct {
	Type              string   `json:"type"`
	Threshold         *float64 `json:"threshold,omitempty"`
	SilenceDurationMs *int     `json:"silence_duration_ms,omitempty"`
}

// NoiseReductionConfig 表示降噪配置
type NoiseReductionConfig struct {
	Type string `json:"type"`
}

// SessionResponse 表示创建 Realtime 会话的响应
type SessionResponse struct {
	ID           string `json:"id"`
	ClientSecret struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
	} `json:"client_secret"`
}

// CreateSession 通过 OpenAI 创建一个新的 Realtime 会话
func (rc *RealtimeClient) CreateSession(ctx context.Context, systemPrompt string) (*ChatSession, error) {
	// 检查是否提供了 OpenAI API 密钥 - 如果缺失则快速失败
	if rc.apiKey == "" {
		return nil, fmt.Errorf("ATC Chat 会话需要 OpenAI API 密钥")
	}

	rc.logger.Info("正在创建新的 OpenAI Realtime 会话",
		logger.String("model", rc.config.Model),
		logger.String("voice", rc.config.Voice))

	// 使用必需的参数创建会话请求
	sessionReq := SessionRequest{
		Model:             rc.config.Model,
		Instructions:      systemPrompt,
		Voice:             rc.config.Voice,
		Modalities:        []string{"text", "audio"},
		InputAudioFormat:  rc.config.InputAudioFormat,
		OutputAudioFormat: rc.config.OutputAudioFormat,
	}

	// 如已配置则添加可选参数
	if rc.config.MaxResponseTokens > 0 {
		sessionReq.MaxResponseTokens = rc.config.MaxResponseTokens
	}

	// OpenAI Realtime API 要求 temperature >= 0.6
	if rc.config.Temperature >= 0.6 {
		sessionReq.Temperature = rc.config.Temperature
	} else {
		// 如果未配置或低于最小值,则使用默认 temperature 0.8
		sessionReq.Temperature = 0.8
	}

	// 根据配置添加轮次检测
	// 如果 TurnDetectionType 为空或为 "none",则完全省略 turn_detection(关闭)
	// 否则,使用指定类型进行配置
	if rc.config.TurnDetectionType != "" && rc.config.TurnDetectionType != "none" {
		turnDetection := &TurnDetectionConfig{
			Type: rc.config.TurnDetectionType,
		}

		if rc.config.VADThreshold > 0 {
			turnDetection.Threshold = &rc.config.VADThreshold
		}

		if rc.config.SilenceDurationMs > 0 {
			turnDetection.SilenceDurationMs = &rc.config.SilenceDurationMs
		}

		sessionReq.TurnDetection = turnDetection
	}
	// 如果为空或为 "none",将 TurnDetection 保留为 nil(从 JSON 中省略)

	// 将请求序列化为 JSON
	jsonData, err := json.Marshal(sessionReq)
	if err != nil {
		return nil, fmt.Errorf("序列化会话请求失败: %w", err)
	}

	// 记录完整的请求载荷
	rc.logger.Info("=== OpenAI 会话创建请求 ===")
	rc.logger.Info("请求 URL: https://api.openai.com/v1/realtime/sessions")
	rc.logger.Info("请求头:",
		logger.String("Content-Type", "application/json"),
		logger.String("Authorization", "Bearer [REDACTED]"),
		logger.String("OpenAI-Beta", "realtime=v1"))
	rc.logger.Info("请求载荷:", logger.String("json", string(jsonData)))

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/realtime/sessions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", rc.apiKey))
	req.Header.Set("OpenAI-Beta", "realtime=v1")

	// 执行请求
	resp, err := rc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("执行 HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态,如果不是 OK 则记录详细错误
	if resp.StatusCode != http.StatusOK {
		// 读取错误响应主体
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			rc.logger.Error("读取错误响应主体失败", logger.Error(readErr))
			return nil, fmt.Errorf("意外的状态码: %d", resp.StatusCode)
		}

		var errorBody map[string]interface{}
		if json.Unmarshal(bodyBytes, &errorBody) == nil {
			rc.logger.Error("OpenAI 会话创建失败,带详细错误",
				logger.Int("status_code", resp.StatusCode),
				logger.Any("error_response", errorBody))
		} else {
			rc.logger.Error("OpenAI 会话创建失败",
				logger.Int("status_code", resp.StatusCode),
				logger.String("response_body", string(bodyBytes)))
		}

		return nil, fmt.Errorf("意外的状态码: %d", resp.StatusCode)
	}

	// 读取响应主体用于日志记录
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应主体失败: %w", err)
	}

	// 记录完整的响应
	rc.logger.Info("=== OpenAI 会话创建响应 ===")
	rc.logger.Info("响应状态:", logger.Int("status_code", resp.StatusCode))
	rc.logger.Info("响应头:")
	for name, values := range resp.Header {
		for _, value := range values {
			rc.logger.Info("  " + name + ": " + value)
		}
	}
	rc.logger.Info("响应载荷:", logger.String("json", string(bodyBytes)))

	// 解析响应
	var sessionResp SessionResponse
	if err := json.Unmarshal(bodyBytes, &sessionResp); err != nil {
		return nil, fmt.Errorf("解码会话响应失败: %w", err)
	}

	// 创建我们自己的会话对象
	chatSession := &ChatSession{
		ID:              generateSessionID(),
		OpenAISessionID: sessionResp.ID,
		ClientSecret:    sessionResp.ClientSecret.Value,
		CreatedAt:       time.Now().UTC(),
		ExpiresAt:       time.Unix(sessionResp.ClientSecret.ExpiresAt, 0),
		Active:          true,
		LastActivity:    time.Now().UTC(),
	}

	rc.logger.Info("成功创建 Realtime 会话",
		logger.String("session_id", chatSession.ID),
		logger.String("openai_session_id", chatSession.OpenAISessionID),
		logger.Time("expires_at", chatSession.ExpiresAt))

	return chatSession, nil
}

// UpdateSessionInstructions 更新现有会话的系统指令
func (rc *RealtimeClient) UpdateSessionInstructions(ctx context.Context, sessionID string, instructions string) error {
	rc.logger.Debug("正在更新会话指令",
		logger.String("session_id", sessionID))

	// 注意:当 Realtime API 支持指令更新时,需要实现此功能
	// 目前,我们只记录该功能尚不可用

	rc.logger.Warn("会话指令更新尚未在 Realtime API 中实现",
		logger.String("session_id", sessionID))

	return nil
}

// EndSession 终止一个 Realtime 会话
func (rc *RealtimeClient) EndSession(ctx context.Context, sessionID string) error {
	rc.logger.Info("正在结束 Realtime 会话",
		logger.String("session_id", sessionID))

	// 注意:Realtime API 可能尚未提供直接的会话终止端点
	// 目前,我们只记录终止操作

	rc.logger.Info("会话已标记为终止",
		logger.String("session_id", sessionID))

	return nil
}

// ValidateSession 检查会话是否仍然有效
func (rc *RealtimeClient) ValidateSession(session *ChatSession) bool {
	if session == nil {
		return false
	}

	// 检查会话是否已过期
	if time.Now().UTC().After(session.ExpiresAt) {
		rc.logger.Debug("会话已过期",
			logger.String("session_id", session.ID),
			logger.Time("expired_at", session.ExpiresAt))
		return false
	}

	// 检查会话是否仍然活跃
	if !session.Active {
		rc.logger.Debug("会话未处于活跃状态",
			logger.String("session_id", session.ID))
		return false
	}

	return true
}

// RefreshSession 创建一个新会话以替换即将过期的会话
func (rc *RealtimeClient) RefreshSession(ctx context.Context, oldSession *ChatSession, systemPrompt string) (*ChatSession, error) {
	rc.logger.Info("正在刷新 Realtime 会话",
		logger.String("old_session_id", oldSession.ID))

	// 创建新会话
	newSession, err := rc.CreateSession(ctx, systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("创建替换会话失败: %w", err)
	}

	// 结束旧会话
	if err := rc.EndSession(ctx, oldSession.OpenAISessionID); err != nil {
		rc.logger.Warn("正确结束旧会话失败",
			logger.String("old_session_id", oldSession.ID),
			logger.Error(err))
	}

	rc.logger.Info("成功刷新会话",
		logger.String("old_session_id", oldSession.ID),
		logger.String("new_session_id", newSession.ID))

	return newSession, nil
}

// GetSessionStatus 返回会话的当前状态
func (rc *RealtimeClient) GetSessionStatus(session *ChatSession) SessionStatus {
	if session == nil {
		return SessionStatus{
			Active:    false,
			Connected: false,
			Error:     "会话为 nil",
		}
	}

	status := SessionStatus{
		ID:           session.ID,
		Active:       session.Active,
		Connected:    rc.ValidateSession(session),
		LastActivity: session.LastActivity,
		ExpiresAt:    session.ExpiresAt,
	}

	if !status.Connected {
		if time.Now().UTC().After(session.ExpiresAt) {
			status.Error = "会话已过期"
		} else if !session.Active {
			status.Error = "会话未激活"
		}
	}

	return status
}

// generateSessionID 生成唯一的会话 ID
func generateSessionID() string {
	return fmt.Sprintf("atc_chat_%d", time.Now().UnixNano())
}

// IsSessionExpiringSoon 检查会话是否会在给定时长内过期
func (rc *RealtimeClient) IsSessionExpiringSoon(session *ChatSession, within time.Duration) bool {
	if session == nil {
		return true
	}

	expiryThreshold := time.Now().UTC().Add(within)
	return session.ExpiresAt.Before(expiryThreshold)
}

// GetTimeUntilExpiry 返回距离会话过期的剩余时间
func (rc *RealtimeClient) GetTimeUntilExpiry(session *ChatSession) time.Duration {
	if session == nil {
		return 0
	}

	return time.Until(session.ExpiresAt)
}
