package templating

import (
	"os"
	"path/filepath"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/frequencies"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/weather"
	"github.com/yegors/co-atc/pkg/logger"
)

const atcRenderedPromptDebugPath = "data/atc_chat_system_prompt_rendered.txt"

// Service 提供主要的模板功能
type Service struct {
	engine     *Engine
	aggregator *DataAggregator
	logger     *logger.Logger
}

// NewService 创建一个新的模板服务
func NewService(
	adsbService *adsb.Service,
	weatherService *weather.Service,
	transcriptionStorage *sqlite.TranscriptionStorage,
	frequencyService *frequencies.Service,
	config *config.Config,
	logger *logger.Logger,
) *Service {
	// 创建数据聚合器
	aggregator := NewDataAggregator(
		adsbService,
		weatherService,
		transcriptionStorage,
		frequencyService,
		config,
		logger,
	)

	// 创建模板引擎
	engine := NewEngine(aggregator, logger)

	return &Service{
		engine:     engine,
		aggregator: aggregator,
		logger:     logger.Named("templating-service"),
	}
}

// RenderATCChatTemplate 使用完整上下文渲染 ATC 聊天模板
func (s *Service) RenderATCChatTemplate(templatePath string) (string, error) {
	opts := ATCChatFormattingOptions()
	rendered, err := s.engine.RenderTemplate(templatePath, opts)
	if err != nil {
		return "", err
	}

	if writeErr := s.writeRenderedATCChatPrompt(rendered); writeErr != nil {
		s.logger.Warn("写入已渲染的 ATC 聊天系统提示词调试文件失败",
			logger.String("path", atcRenderedPromptDebugPath),
			logger.Error(writeErr))
	}

	return rendered, nil
}

func (s *Service) writeRenderedATCChatPrompt(rendered string) error {
	if err := os.MkdirAll(filepath.Dir(atcRenderedPromptDebugPath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(atcRenderedPromptDebugPath, []byte(rendered), 0o644)
}

// RenderPostProcessorTemplate 渲染后处理器模板,不包含转写历史
func (s *Service) RenderPostProcessorTemplate(templatePath string) (string, error) {
	opts := PostProcessorFormattingOptions()
	return s.engine.RenderTemplate(templatePath, opts)
}

// RenderTemplate 使用自定义格式化选项渲染模板
func (s *Service) RenderTemplate(templatePath string, opts FormattingOptions) (string, error) {
	return s.engine.RenderTemplate(templatePath, opts)
}

// GetTemplateContext 获取当前空域上下文用于自定义处理
func (s *Service) GetTemplateContext(opts FormattingOptions) (*TemplateContext, error) {
	return s.aggregator.GetTemplateContext(opts)
}

// RenderTemplateWithContext 使用预聚合的上下文渲染模板
func (s *Service) RenderTemplateWithContext(templatePath string, context *TemplateContext, opts FormattingOptions) (string, error) {
	return s.engine.RenderTemplateWithContext(templatePath, context, opts)
}

// ReloadTemplate 强制从文件重新加载模板
func (s *Service) ReloadTemplate(templatePath string) error {
	return s.engine.ReloadTemplate(templatePath)
}

// ReloadAllTemplates 强制重新加载所有缓存的模板
func (s *Service) ReloadAllTemplates() error {
	return s.engine.ReloadAllTemplates()
}

// ClearCache 清除模板缓存
func (s *Service) ClearCache() {
	s.engine.ClearCache()
}

// GetCacheStats 返回模板缓存的统计信息
func (s *Service) GetCacheStats() map[string]interface{} {
	return s.engine.GetCacheStats()
}

// GetRawTemplate 返回未经处理的原始模板内容
func (s *Service) GetRawTemplate(templatePath string) (string, error) {
	return s.engine.GetRawTemplate(templatePath)
}
