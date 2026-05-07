package templating

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"sync"
	"text/template"

	"github.com/yegors/co-atc/pkg/logger"
)

// Engine 处理模板的加载、缓存和渲染
type Engine struct {
	aggregator    *DataAggregator
	templateCache map[string]*template.Template
	cacheMutex    sync.RWMutex
	logger        *logger.Logger
}

// NewEngine 创建一个新的模板引擎
func NewEngine(aggregator *DataAggregator, logger *logger.Logger) *Engine {
	return &Engine{
		aggregator:    aggregator,
		templateCache: make(map[string]*template.Template),
		logger:        logger.Named("template-engine"),
	}
}

// RenderTemplate 使用当前空域数据渲染模板
func (e *Engine) RenderTemplate(templatePath string, opts FormattingOptions) (string, error) {
	e.logger.Debug("正在渲染模板",
		logger.String("template_path", templatePath),
		logger.Int("max_aircraft", opts.MaxAircraft),
		logger.Bool("include_weather", opts.IncludeWeather),
		logger.Bool("include_runways", opts.IncludeRunways),
		logger.Bool("include_transcription_history", opts.IncludeTranscriptionHistory))

	// 如果不在缓存中则加载模板
	tmpl, err := e.getTemplate(templatePath)
	if err != nil {
		return "", fmt.Errorf("获取模板失败: %w", err)
	}

	// 从聚合器获取模板上下文
	context, err := e.aggregator.GetTemplateContext(opts)
	if err != nil {
		return "", fmt.Errorf("获取模板上下文失败: %w", err)
	}

	// 格式化数据用于模板渲染
	data := e.prepareTemplateData(context, opts)

	// 渲染模板
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("执行模板失败: %w", err)
	}

	rendered := buf.String()
	e.logger.Debug("模板渲染成功",
		logger.String("template_path", templatePath),
		logger.Int("rendered_length", len(rendered)))

	return rendered, nil
}

// RenderTemplateWithContext 使用预聚合的上下文数据渲染模板
func (e *Engine) RenderTemplateWithContext(templatePath string, context *TemplateContext, opts FormattingOptions) (string, error) {
	e.logger.Debug("使用提供的上下文渲染模板",
		logger.String("template_path", templatePath))

	// 如果不在缓存中则加载模板
	tmpl, err := e.getTemplate(templatePath)
	if err != nil {
		return "", fmt.Errorf("获取模板失败: %w", err)
	}

	// 格式化数据用于模板渲染
	data := e.prepareTemplateData(context, opts)

	// 渲染模板
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("执行模板失败: %w", err)
	}

	rendered := buf.String()
	e.logger.Debug("使用上下文成功渲染模板",
		logger.String("template_path", templatePath),
		logger.Int("rendered_length", len(rendered)))

	return rendered, nil
}

// prepareTemplateData 将原始上下文数据转换为已格式化的模板数据
func (e *Engine) prepareTemplateData(context *TemplateContext, opts FormattingOptions) TemplateData {
	data := TemplateData{
		Timestamp: context.Timestamp,
		Time:      context.Timestamp.Format(opts.TimeFormat),
	}

	// 格式化飞行器数据
	data.Aircraft = FormatAircraftData(context.Aircraft, context.Airport)

	// 如果有可用的气象数据,则进行格式化
	if opts.IncludeWeather && context.Weather != nil {
		data.Weather = FormatWeatherData(context.Weather)
	} else {
		data.Weather = "Weather data not available."
	}

	// 如果有可用的跑道数据,则进行格式化
	if opts.IncludeRunways {
		data.Runways = FormatRunwayData(context.Runways)
		data.ActiveRunways = FormatActiveRunwaysData(context.ActiveRunways)
	} else {
		data.Runways = "Runway information not available."
		data.ActiveRunways = "Active runway detection not available."
	}

	// 如果请求,格式化转写历史(仅用于 ATC 聊天)
	if opts.IncludeTranscriptionHistory {
		data.TranscriptionHistory = FormatTranscriptionHistory(context.TranscriptionHistory)
	} else {
		data.TranscriptionHistory = ""
	}

	// 格式化机场数据
	data.Airport = FormatAirportData(context.Airport)

	return data
}

// getTemplate 从缓存中检索模板,或从文件加载
func (e *Engine) getTemplate(templatePath string) (*template.Template, error) {
	// 先检查缓存(读锁)
	e.cacheMutex.RLock()
	if tmpl, exists := e.templateCache[templatePath]; exists {
		e.cacheMutex.RUnlock()
		return tmpl, nil
	}
	e.cacheMutex.RUnlock()

	// 模板不在缓存中,加载它(写锁)
	e.cacheMutex.Lock()
	defer e.cacheMutex.Unlock()

	// 双重检查,以防另一个 goroutine 在我们等待时已加载它
	if tmpl, exists := e.templateCache[templatePath]; exists {
		return tmpl, nil
	}

	// 从文件加载模板
	tmpl, err := e.loadTemplate(templatePath)
	if err != nil {
		return nil, err
	}

	// 缓存模板
	e.templateCache[templatePath] = tmpl
	e.logger.Debug("模板已加载并缓存",
		logger.String("template_path", templatePath))

	return tmpl, nil
}

// loadTemplate 从文件加载模板
func (e *Engine) loadTemplate(templatePath string) (*template.Template, error) {
	content, err := ioutil.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("读取模板文件 '%s' 失败: %w", templatePath, err)
	}

	tmpl, err := template.New(templatePath).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("解析模板文件 '%s' 失败: %w", templatePath, err)
	}

	return tmpl, nil
}

// ReloadTemplate 强制从文件重新加载模板
func (e *Engine) ReloadTemplate(templatePath string) error {
	e.cacheMutex.Lock()
	defer e.cacheMutex.Unlock()

	// 从文件加载模板
	tmpl, err := e.loadTemplate(templatePath)
	if err != nil {
		return err
	}

	// 更新缓存
	e.templateCache[templatePath] = tmpl
	e.logger.Info("模板已重新加载",
		logger.String("template_path", templatePath))

	return nil
}

// ReloadAllTemplates 强制从文件重新加载所有缓存的模板
func (e *Engine) ReloadAllTemplates() error {
	e.cacheMutex.Lock()
	defer e.cacheMutex.Unlock()

	var errors []string
	reloadedCount := 0

	for templatePath := range e.templateCache {
		tmpl, err := e.loadTemplate(templatePath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", templatePath, err))
			continue
		}
		e.templateCache[templatePath] = tmpl
		reloadedCount++
	}

	if len(errors) > 0 {
		e.logger.Error("部分模板重新加载失败",
			logger.Int("successful", reloadedCount),
			logger.Int("failed", len(errors)))
		return fmt.Errorf("重新加载 %d 个模板失败: %v", len(errors), errors)
	}

	e.logger.Info("所有模板均已成功重新加载",
		logger.Int("count", reloadedCount))

	return nil
}

// ClearCache 清除模板缓存
func (e *Engine) ClearCache() {
	e.cacheMutex.Lock()
	defer e.cacheMutex.Unlock()

	templateCount := len(e.templateCache)
	e.templateCache = make(map[string]*template.Template)

	e.logger.Info("模板缓存已清除",
		logger.Int("cleared_count", templateCount))
}

// GetCacheStats 返回模板缓存的统计信息
func (e *Engine) GetCacheStats() map[string]interface{} {
	e.cacheMutex.RLock()
	defer e.cacheMutex.RUnlock()

	templates := make([]string, 0, len(e.templateCache))
	for path := range e.templateCache {
		templates = append(templates, path)
	}

	return map[string]interface{}{
		"cached_template_count": len(e.templateCache),
		"cached_templates":      templates,
	}
}

// GetRawTemplate 返回未经处理的原始模板内容
func (e *Engine) GetRawTemplate(templatePath string) (string, error) {
	content, err := ioutil.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("读取模板文件 '%s' 失败: %w", templatePath, err)
	}
	return string(content), nil
}
