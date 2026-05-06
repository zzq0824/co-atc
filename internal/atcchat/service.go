package atcchat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/templating"
	"github.com/yegors/co-atc/pkg/logger"
)

// TemplatingService 共享模板功能的接口
type TemplatingService interface {
	RenderATCChatTemplate(templatePath string) (string, error)
	GetTemplateContext(opts FormattingOptions) (*TemplateContext, error)
}

// 引入 templating 类型
type FormattingOptions = templating.FormattingOptions
type TemplateContext = templating.TemplateContext

// ATCChatFormattingOptions 返回 ATC 聊天的格式化选项
func ATCChatFormattingOptions() FormattingOptions {
	return templating.ATCChatFormattingOptions()
}

// Service 管理 ATC 聊天会话与交互
type Service struct {
	realtimeClient    *RealtimeClient
	templatingService TemplatingService
	config            *config.ATCChatConfig
	logger            *logger.Logger

	// 会话管理
	sessions   map[string]*ChatSession
	sessionsMu sync.RWMutex

	// 用于发送更新的 WebSocket 连接注册表
	wsConnections   map[string]chan string // sessionID -> 更新通道
	wsConnectionsMu sync.RWMutex

	// 后台任务
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewService 创建一个新的 ATC 聊天服务
func NewService(
	templatingService TemplatingService,
	config *config.Config,
	logger *logger.Logger,
) (*Service, error) {
	if !config.ATCChat.Enabled {
		return nil, fmt.Errorf("ATC 聊天在配置中已禁用")
	}

	// 创建会话配置
	sessionConfig := SessionConfig{
		InputAudioFormat:  config.ATCChat.InputAudioFormat,
		OutputAudioFormat: config.ATCChat.OutputAudioFormat,
		SampleRate:        config.ATCChat.SampleRate,
		Channels:          config.ATCChat.Channels,
		MaxResponseTokens: config.ATCChat.MaxResponseTokens,
		Temperature:       config.ATCChat.Temperature,
		TurnDetectionType: config.ATCChat.TurnDetectionType,
		VADThreshold:      config.ATCChat.VADThreshold,
		SilenceDurationMs: config.ATCChat.SilenceDurationMs,
		Voice:             config.ATCChat.Voice,
		Model:             config.ATCChat.RealtimeModel,
	}

	// 创建 Realtime 客户端
	realtimeClient := NewRealtimeClient(
		config.ATCChat.OpenAIAPIKey,
		sessionConfig,
		logger,
	)

	ctx, cancel := context.WithCancel(context.Background())

	service := &Service{
		realtimeClient:    realtimeClient,
		templatingService: templatingService,
		config:            &config.ATCChat,
		logger:            logger.Named("atc-chat-service"),
		sessions:          make(map[string]*ChatSession),
		wsConnections:     make(map[string]chan string),
		ctx:               ctx,
		cancel:            cancel,
	}

	// 启动后台任务
	service.startBackgroundTasks()

	return service, nil
}

// CreateSession 创建一个新的聊天会话
func (s *Service) CreateSession(ctx context.Context) (*ChatSession, error) {
	s.logger.Info("正在创建新的 ATC 聊天会话")

	// 使用模板服务渲染 ATC 聊天模板
	staticPrompt, err := s.templatingService.RenderATCChatTemplate(s.config.SystemPromptPath)
	if err != nil {
		return nil, fmt.Errorf("渲染 ATC 聊天模板失败: %w", err)
	}

	// 通过 REST API 使用静态指令创建 OpenAI 会话
	s.logger.Info("通过 REST API 使用静态指令创建 OpenAI 会话",
		logger.Int("prompt_length", len(staticPrompt)))

	session, err := s.realtimeClient.CreateSession(ctx, staticPrompt)
	if err != nil {
		return nil, fmt.Errorf("创建 OpenAI 会话失败: %w", err)
	}

	// 存储会话
	s.sessionsMu.Lock()
	s.sessions[session.ID] = session
	s.sessionsMu.Unlock()

	s.logger.Info("成功创建带 OpenAI 会话的 ATC 聊天会话",
		logger.String("session_id", session.ID),
		logger.String("openai_session_id", session.OpenAISessionID),
		logger.Int("total_sessions", len(s.sessions)))

	return session, nil
}

// GetSession 通过 ID 获取会话
func (s *Service) GetSession(sessionID string) (*ChatSession, error) {
	s.sessionsMu.RLock()
	session, exists := s.sessions[sessionID]
	s.sessionsMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("未找到会话: %s", sessionID)
	}

	// 校验会话
	if !s.realtimeClient.ValidateSession(session) {
		return nil, fmt.Errorf("会话无效或已过期: %s", sessionID)
	}

	return session, nil
}

// EndSession 终止一个聊天会话
func (s *Service) EndSession(ctx context.Context, sessionID string) error {
	s.logger.Info("正在结束 ATC 聊天会话",
		logger.String("session_id", sessionID))

	s.sessionsMu.Lock()
	session, exists := s.sessions[sessionID]
	if exists {
		delete(s.sessions, sessionID)
	}
	s.sessionsMu.Unlock()

	if !exists {
		return fmt.Errorf("未找到会话: %s", sessionID)
	}

	// 注销 WebSocket 连接以停止接收更新
	s.UnregisterWebSocketConnection(sessionID)

	// 结束 OpenAI 会话
	if err := s.realtimeClient.EndSession(ctx, session.OpenAISessionID); err != nil {
		s.logger.Error("结束 OpenAI 会话失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		// 即使 OpenAI 会话终止失败也继续清理
	}

	// 将会话标记为非活跃
	session.Active = false

	s.logger.Info("成功结束 ATC 聊天会话",
		logger.String("session_id", sessionID),
		logger.Int("remaining_sessions", len(s.sessions)))

	return nil
}

// GetSessionStatus 返回会话的状态
func (s *Service) GetSessionStatus(sessionID string) (SessionStatus, error) {
	s.sessionsMu.RLock()
	session, exists := s.sessions[sessionID]
	s.sessionsMu.RUnlock()

	if !exists {
		return SessionStatus{
			ID:        sessionID,
			Active:    false,
			Connected: false,
			Error:     "未找到会话",
		}, nil
	}

	return s.realtimeClient.GetSessionStatus(session), nil
}

// ListActiveSessions 返回所有活跃会话
func (s *Service) ListActiveSessions() []*ChatSession {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	var activeSessions []*ChatSession
	for _, session := range s.sessions {
		// 同时检查 OpenAI 会话有效性以及 WebSocket 连接
		if s.realtimeClient.ValidateSession(session) && s.hasActiveWebSocketConnection(session.ID) {
			activeSessions = append(activeSessions, session)
		}
	}

	return activeSessions
}

// UpdateSessionContext 使用最新的空域数据更新会话的系统提示词
func (s *Service) UpdateSessionContext(ctx context.Context, sessionID string) error {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}

	// 使用共享模板服务渲染更新后的系统提示词
	systemPrompt, err := s.templatingService.RenderATCChatTemplate(s.config.SystemPromptPath)
	if err != nil {
		return fmt.Errorf("渲染系统提示词失败: %w", err)
	}

	// 更新会话指令
	if err := s.realtimeClient.UpdateSessionInstructions(ctx, session.OpenAISessionID, systemPrompt); err != nil {
		return fmt.Errorf("更新会话指令失败: %w", err)
	}

	// 更新最后活动时间
	session.LastActivity = time.Now().UTC()

	s.logger.Debug("已更新会话上下文",
		logger.String("session_id", sessionID))

	return nil
}

// GetAirspaceStatus 返回当前空域状态
func (s *Service) GetAirspaceStatus() map[string]interface{} {
	s.sessionsMu.RLock()
	sessionCount := len(s.sessions)
	s.sessionsMu.RUnlock()

	return map[string]interface{}{
		"active_sessions":    sessionCount,
		"templating_enabled": true,
	}
}

// startBackgroundTasks 启动后台维护任务
func (s *Service) startBackgroundTasks() {
	s.wg.Add(1)
	go s.sessionCleanupTask()

	// 如已启用,启动自动系统提示词刷新任务
	if s.config.RefreshSystemPromptSecs > 0 {
		s.wg.Add(1)
		go s.systemPromptRefreshTask()
	}
}

// sessionCleanupTask 周期性清理过期会话
func (s *Service) sessionCleanupTask() {
	defer s.wg.Done()

	ticker := time.NewTicker(5 * time.Minute) // 每 5 分钟检查一次
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpiredSessions()
		}
	}
}

// systemPromptRefreshTask 周期性向所有活跃会话发送系统提示词更新
func (s *Service) systemPromptRefreshTask() {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Duration(s.config.RefreshSystemPromptSecs) * time.Second)
	defer ticker.Stop()

	s.logger.Info("已启动自动系统提示词刷新任务",
		logger.Int("interval_seconds", s.config.RefreshSystemPromptSecs))

	for {
		select {
		case <-s.ctx.Done():
			s.logger.Info("系统提示词刷新任务正在关闭")
			return
		case <-ticker.C:
			s.refreshAllActiveSessions()
		}
	}
}

// refreshAllActiveSessions 向所有活跃会话发送系统提示词更新
func (s *Service) refreshAllActiveSessions() {
	activeSessions := s.ListActiveSessions()

	if len(activeSessions) == 0 {
		s.logger.Debug("没有活跃会话需要刷新")
		return
	}

	s.logger.Info("正在为所有活跃会话刷新系统提示词",
		logger.Int("session_count", len(activeSessions)))

	for _, session := range activeSessions {
		if err := s.sendSystemPromptUpdate(session.ID); err != nil {
			s.logger.Error("向会话发送系统提示词更新失败",
				logger.String("session_id", session.ID),
				logger.Error(err))
		}
	}
}

// sendSystemPromptUpdate 向特定会话发送系统提示词更新,并带详细日志
func (s *Service) sendSystemPromptUpdate(sessionID string) error {
	// 处理前先检查会话是否仍存在
	_, err := s.GetSession(sessionID)
	if err != nil {
		s.logger.Debug("跳过对不存在会话的系统提示词更新",
			logger.String("session_id", sessionID),
			logger.Error(err))
		return nil // 不视为错误,只是跳过
	}

	// 使用当前空域数据生成最新的系统提示词,并获取变量
	promptWithVars, err := s.GenerateSystemPromptWithVariables(sessionID)
	if err != nil {
		return fmt.Errorf("生成带变量的系统提示词失败: %w", err)
	}

	// 在 info 级别记录所有模板化变量,并使用合适的格式
	fmt.Printf("\n=== 系统提示词刷新 - 模板化变量(会话: %s) ===\n", sessionID)
	fmt.Printf("\n飞行器数据:\n%v\n", promptWithVars.Variables["Aircraft"])
	fmt.Printf("\n气象数据:\n%v\n", promptWithVars.Variables["Weather"])
	fmt.Printf("\n跑道数据:\n%v\n", promptWithVars.Variables["Runways"])
	fmt.Printf("\n转写历史:\n%v\n", promptWithVars.Variables["TranscriptionHistory"])
	fmt.Printf("\n机场数据:\n%v\n", promptWithVars.Variables["Airport"])
	fmt.Printf("=== 模板化变量结束 ===\n\n")

	// 创建 session.update 消息
	sessionUpdate := map[string]interface{}{
		"type": "session.update",
		"session": map[string]interface{}{
			"instructions": promptWithVars.Prompt,
		},
	}

	// 转换为 JSON
	updateData, err := json.Marshal(sessionUpdate)
	if err != nil {
		return fmt.Errorf("序列化会话更新失败: %w", err)
	}

	// 通过 WebSocket 通道发送更新
	s.SendSessionUpdate(sessionID, string(updateData))

	s.logger.Info("成功发送自动系统提示词更新",
		logger.String("session_id", sessionID),
		logger.Int("prompt_length", len(promptWithVars.Prompt)))

	return nil
}

// UpdateSessionContextOnDemand 使用最新的空域数据更新特定会话的上下文
// 当用户开始说话(按键说话)时调用,以确保使用最新数据
func (s *Service) UpdateSessionContextOnDemand(sessionID string) error {
	s.logger.Debug("按需更新会话上下文以处理用户交互",
		logger.String("session_id", sessionID))

	// 使用当前空域数据生成最新的系统提示词
	systemPrompt, err := s.GenerateSystemPrompt(sessionID)
	if err != nil {
		s.logger.Error("为按需更新生成系统提示词失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		return fmt.Errorf("生成系统提示词失败: %w", err)
	}

	// 创建 session.update 消息
	sessionUpdate := map[string]interface{}{
		"type": "session.update",
		"session": map[string]interface{}{
			"instructions": systemPrompt,
		},
	}

	// 转换为 JSON
	updateData, err := json.Marshal(sessionUpdate)
	if err != nil {
		s.logger.Error("为按需更新序列化会话更新失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		return fmt.Errorf("序列化会话更新失败: %w", err)
	}

	// 通过 WebSocket 通道发送更新
	s.SendSessionUpdate(sessionID, string(updateData))

	s.logger.Info("成功发送按需上下文更新",
		logger.String("session_id", sessionID))

	return nil
}

// cleanupExpiredSessions 移除过期或无效的会话
func (s *Service) cleanupExpiredSessions() {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()

	var expiredSessions []string
	for sessionID, session := range s.sessions {
		if !s.realtimeClient.ValidateSession(session) {
			expiredSessions = append(expiredSessions, sessionID)
		}
	}

	for _, sessionID := range expiredSessions {
		delete(s.sessions, sessionID)
		s.logger.Debug("已清理过期会话",
			logger.String("session_id", sessionID))
	}

	if len(expiredSessions) > 0 {
		s.logger.Info("已清理过期会话",
			logger.Int("expired_count", len(expiredSessions)),
			logger.Int("remaining_sessions", len(s.sessions)))
	}
}

// Shutdown 优雅关闭服务
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("正在关闭 ATC 聊天服务")

	// 取消后台任务
	s.cancel()

	// 等待后台任务完成
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Debug("后台任务已完成")
	case <-ctx.Done():
		s.logger.Warn("达到关闭超时,强制退出")
	}

	// 结束所有活跃会话
	s.sessionsMu.Lock()
	sessionIDs := make([]string, 0, len(s.sessions))
	for sessionID := range s.sessions {
		sessionIDs = append(sessionIDs, sessionID)
	}
	s.sessionsMu.Unlock()

	for _, sessionID := range sessionIDs {
		if err := s.EndSession(ctx, sessionID); err != nil {
			s.logger.Error("关闭过程中结束会话失败",
				logger.String("session_id", sessionID),
				logger.Error(err))
		}
	}

	s.logger.Info("ATC 聊天服务关闭完成")
	return nil
}

// GetSessionCount 返回活跃会话的数量
func (s *Service) GetSessionCount() int {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	return len(s.sessions)
}

// GenerateSystemPrompt 使用当前空域数据生成模板化的系统提示词
func (s *Service) GenerateSystemPrompt(sessionID string) (string, error) {
	s.logger.Debug("为会话生成系统提示词",
		logger.String("session_id", sessionID))

	// 使用共享模板服务生成提示词
	prompt, err := s.templatingService.RenderATCChatTemplate(s.config.SystemPromptPath)
	if err != nil {
		s.logger.Error("从模板生成提示词失败", logger.Error(err))
		return "", fmt.Errorf("生成提示词失败: %w", err)
	}

	s.logger.Info("已为 ATC 聊天生成系统提示词",
		logger.String("session_id", sessionID),
		logger.Int("prompt_length", len(prompt)))

	// 记录提示词生成但不输出完整内容,以减少日志冗长
	s.logger.Debug("系统提示词生成成功",
		logger.String("session_id", sessionID))

	return prompt, nil
}

// PromptWithVariables 同时包含已渲染的提示词与单独的模板变量
type PromptWithVariables struct {
	Prompt    string                 `json:"prompt"`
	Variables map[string]interface{} `json:"variables"`
}

// GenerateSystemPromptWithVariables 生成模板化的系统提示词并返回单独的变量
func (s *Service) GenerateSystemPromptWithVariables(sessionID string) (*PromptWithVariables, error) {
	s.logger.Debug("为会话生成带变量的系统提示词",
		logger.String("session_id", sessionID))

	// 使用共享模板服务生成提示词
	prompt, err := s.templatingService.RenderATCChatTemplate(s.config.SystemPromptPath)
	if err != nil {
		s.logger.Error("从模板生成提示词失败", logger.Error(err))
		return nil, fmt.Errorf("生成提示词失败: %w", err)
	}

	// 获取实际的模板上下文以返回真实的变量数据
	context, err := s.templatingService.GetTemplateContext(ATCChatFormattingOptions())
	if err != nil {
		s.logger.Error("获取模板上下文以填充变量失败", logger.Error(err))
		// 如果上下文获取失败,回退到简化的变量
		variables := map[string]interface{}{
			"Aircraft":             "获取飞行器数据时出错",
			"Weather":              "获取气象数据时出错",
			"Runways":              "获取跑道数据时出错",
			"ActiveRunways":        "获取活跃跑道数据时出错",
			"TranscriptionHistory": "获取转写数据时出错",
			"Airport":              "获取机场数据时出错",
		}
		return &PromptWithVariables{
			Prompt:    prompt,
			Variables: variables,
		}, nil
	}

	// 格式化实际模板变量以便展示
	variables := map[string]interface{}{
		"Aircraft":             templating.FormatAircraftData(context.Aircraft, context.Airport),
		"Weather":              templating.FormatWeatherData(context.Weather),
		"Runways":              templating.FormatRunwayData(context.Runways),
		"ActiveRunways":        templating.FormatActiveRunwaysData(context.ActiveRunways),
		"TranscriptionHistory": templating.FormatTranscriptionHistory(context.TranscriptionHistory),
		"Airport":              templating.FormatAirportData(context.Airport),
	}

	s.logger.Info("已为 ATC 聊天生成带变量的系统提示词",
		logger.String("session_id", sessionID),
		logger.Int("prompt_length", len(prompt)))

	return &PromptWithVariables{
		Prompt:    prompt,
		Variables: variables,
	}, nil
}

// GetRealtimeModel 返回已配置的 Realtime 模型
func (s *Service) GetRealtimeModel() string {
	return s.config.RealtimeModel
}

// IsEnabled 返回 ATC 聊天服务是否已启用
func (s *Service) IsEnabled() bool {
	return s.config.Enabled
}

// GetConfig 返回 ATC 聊天配置
func (s *Service) GetConfig() *config.ATCChatConfig {
	return s.config
}

// RegisterWebSocketConnection 为会话更新注册一个 WebSocket 连接
func (s *Service) RegisterWebSocketConnection(sessionID string) chan string {
	s.wsConnectionsMu.Lock()
	defer s.wsConnectionsMu.Unlock()

	updateChan := make(chan string, 10) // 更新缓冲区
	s.wsConnections[sessionID] = updateChan

	s.logger.Debug("已为会话更新注册 WebSocket 连接",
		logger.String("session_id", sessionID))

	return updateChan
}

// UnregisterWebSocketConnection 从注册表中移除一个 WebSocket 连接
func (s *Service) UnregisterWebSocketConnection(sessionID string) {
	s.wsConnectionsMu.Lock()
	defer s.wsConnectionsMu.Unlock()

	if updateChan, exists := s.wsConnections[sessionID]; exists {
		close(updateChan)
		delete(s.wsConnections, sessionID)

		s.logger.Debug("已为会话更新注销 WebSocket 连接",
			logger.String("session_id", sessionID))
	}
}

// hasActiveWebSocketConnection 检查会话是否有活跃的 WebSocket 连接
func (s *Service) hasActiveWebSocketConnection(sessionID string) bool {
	s.wsConnectionsMu.RLock()
	defer s.wsConnectionsMu.RUnlock()

	_, exists := s.wsConnections[sessionID]
	return exists
}

// SendSessionUpdate 向特定会话的 WebSocket 连接发送会话更新
func (s *Service) SendSessionUpdate(sessionID string, updateMessage string) {
	s.wsConnectionsMu.RLock()
	updateChan, exists := s.wsConnections[sessionID]
	s.wsConnectionsMu.RUnlock()

	if exists {
		select {
		case updateChan <- updateMessage:
			s.logger.Debug("已向 WebSocket 发送会话更新",
				logger.String("session_id", sessionID))
		default:
			s.logger.Warn("发送会话更新失败 - 通道已满",
				logger.String("session_id", sessionID))
		}
	}
}
