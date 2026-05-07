package weather

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// Service 管理气象数据的获取与缓存
type Service struct {
	config      WeatherConfig
	airportCode string
	client      *Client
	cache       *Cache
	logger      *logger.Logger

	// 服务生命周期
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool
	mu      sync.RWMutex

	// 初始数据就绪
	initialDataReady chan struct{}
	initialDataOnce  sync.Once
}

// NewService 创建一个新的气象服务
func NewService(configWeather ConfigWeatherConfig, airportCode string, logger *logger.Logger) *Service {
	// 将 config 转换为内部 WeatherConfig 类型
	weatherConfig := FromConfigWeatherConfig(configWeather)

	ctx, cancel := context.WithCancel(context.Background())

	return &Service{
		config:           weatherConfig,
		airportCode:      airportCode,
		client:           NewClient(weatherConfig, logger),
		cache:            NewCache(weatherConfig, logger),
		logger:           logger.Named("weather-service"),
		ctx:              ctx,
		cancel:           cancel,
		initialDataReady: make(chan struct{}),
	}
}

// Start 启动气象服务的后台操作
func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return nil // 已启动
	}

	s.logger.Info("正在启动气象服务",
		logger.String("airport", s.airportCode),
		logger.Int("refresh_interval_minutes", s.config.RefreshIntervalMinutes))

	// 执行首次获取
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.performInitialFetch()
	}()

	// 启动后台刷新 goroutine
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.backgroundRefresh()
	}()

	s.started = true
	return nil
}

// Stop 优雅地关闭气象服务
func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return nil // 已停止
	}

	s.logger.Info("正在停止气象服务")

	// 取消 context,通知 goroutine 停止
	s.cancel()

	// 等待所有 goroutine 完成
	s.wg.Wait()

	s.started = false
	s.logger.Info("气象服务已停止")
	return nil
}

// GetWeatherData 返回当前缓存的气象数据
// 如果服务刚启动,会等待初始数据可用
func (s *Service) GetWeatherData() *WeatherData {
	// 等待初始数据就绪(带超时)
	select {
	case <-s.initialDataReady:
		// 初始数据已就绪,继续正常处理
	case <-time.After(30 * time.Second):
		// 等待初始数据超时,记录警告并返回错误数据
		s.logger.Warn("等待初始气象数据超时")
		return &WeatherData{
			LastUpdated: time.Now(),
			FetchErrors: []string{"气象数据仍在获取中,请稍后再试"},
		}
	}

	data := s.cache.Get()
	if data == nil {
		// 在初始数据就绪后通常不应发生,但要优雅处理
		s.logger.Warn("初始获取完成后仍无气象数据可用")
		return &WeatherData{
			LastUpdated: time.Now(),
			FetchErrors: []string{"气象数据暂时不可用"},
		}
	}

	return data
}

// RefreshNow 触发气象数据的立即刷新
func (s *Service) RefreshNow() {
	s.logger.Info("已触发手动气象刷新")
	go s.fetchAndUpdateCache()
}

// GetCacheStats 返回缓存统计信息
func (s *Service) GetCacheStats() map[string]interface{} {
	return s.cache.GetStats()
}

// IsStarted 返回服务当前是否在运行
func (s *Service) IsStarted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started
}

// performInitialFetch 在服务启动时执行第一次气象数据获取
func (s *Service) performInitialFetch() {
	s.logger.Info("正在执行初始气象数据获取",
		logger.String("airport", s.airportCode))

	s.fetchAndUpdateCache()

	// 通知初始数据已就绪
	s.initialDataOnce.Do(func() {
		close(s.initialDataReady)
		s.logger.Info("初始气象数据获取完成")
	})
}

// backgroundRefresh 运行周期性的气象数据刷新
func (s *Service) backgroundRefresh() {
	refreshInterval := time.Duration(s.config.RefreshIntervalMinutes) * time.Minute
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	s.logger.Info("后台气象刷新已启动",
		logger.String("interval", refreshInterval.String()))

	for {
		select {
		case <-s.ctx.Done():
			s.logger.Info("后台气象刷新已停止")
			return
		case <-ticker.C:
			s.logger.Debug("已触发周期性气象刷新")
			s.fetchAndUpdateCache()
		}
	}
}

// fetchAndUpdateCache 获取气象数据并更新缓存
func (s *Service) fetchAndUpdateCache() {
	startTime := time.Now()

	s.logger.Debug("正在获取气象数据",
		logger.String("airport", s.airportCode))

	// 获取所有已启用的气象数据类型
	results := s.client.FetchAll(s.airportCode)

	// 用结果更新缓存
	s.cache.Update(results, s.airportCode)

	duration := time.Since(startTime)
	s.logger.Info("气象数据获取完成",
		logger.String("airport", s.airportCode),
		logger.String("duration", duration.String()),
		logger.Int("total_requests", len(results)))
}

// ValidateConfig 校验气象服务配置
func ValidateConfig(config WeatherConfig) error {
	if config.RefreshIntervalMinutes <= 0 {
		return fmt.Errorf("refresh_interval_minutes 必须大于 0")
	}

	if config.RequestTimeoutSeconds <= 0 {
		return fmt.Errorf("request_timeout_seconds 必须大于 0")
	}

	if config.MaxRetries < 0 {
		return fmt.Errorf("max_retries 必须为 0 或更大")
	}

	if config.CacheExpiryMinutes <= 0 {
		return fmt.Errorf("cache_expiry_minutes 必须大于 0")
	}

	if config.APIBaseURL == "" {
		return fmt.Errorf("api_base_url 不能为空")
	}

	// 至少必须启用一种气象类型
	if !config.FetchMETAR && !config.FetchTAF && !config.FetchNOTAMs {
		return fmt.Errorf("至少必须启用一种气象类型(fetch_metar、fetch_taf 或 fetch_notams)")
	}

	return nil
}
