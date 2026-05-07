package weather

import (
	"fmt"
	"sync"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// Cache 通过线程安全的操作管理气象数据缓存
type Cache struct {
	cache  *WeatherCache
	config WeatherConfig
	logger *logger.Logger
	mu     sync.RWMutex
}

// NewCache 创建一个新的气象缓存管理器
func NewCache(config WeatherConfig, logger *logger.Logger) *Cache {
	return &Cache{
		cache:  NewWeatherCache(),
		config: config,
		logger: logger.Named("weather-cache"),
	}
}

// Get 返回当前缓存的气象数据
// 如果尚未获取过数据则返回 nil
func (c *Cache) Get() *WeatherData {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data := c.cache.Get()
	if data == nil {
		return nil
	}

	// 检查是否仅是默认的空数据(没有实际获取到气象数据)
	if data.METAR == nil && data.TAF == nil && data.NOTAMs == nil && len(data.FetchErrors) == 0 {
		return nil
	}

	return data
}

// Set 用新的气象数据更新缓存
func (c *Cache) Set(data *WeatherData) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiryDuration := time.Duration(c.config.CacheExpiryMinutes) * time.Minute
	c.cache.Set(data, expiryDuration)

	c.logger.Debug("气象数据已缓存",
		logger.Time("last_updated", data.LastUpdated),
		logger.Time("expires_at", time.Now().Add(expiryDuration)),
		logger.Int("error_count", len(data.FetchErrors)))
}

// IsExpired 检查缓存的数据是否已过期
func (c *Cache) IsExpired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache.IsExpired()
}

// Update 用新的获取结果更新缓存
func (c *Cache) Update(results []FetchResult, airportCode string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 获取当前数据或新建
	currentData := c.cache.Get()
	if currentData == nil {
		currentData = &WeatherData{}
	}

	// 创建新的数据结构
	newData := &WeatherData{
		METAR:       currentData.METAR,
		TAF:         currentData.TAF,
		NOTAMs:      currentData.NOTAMs,
		LastUpdated: time.Now(),
		FetchErrors: []string{},
	}

	// 处理获取结果
	for _, result := range results {
		switch result.Type {
		case WeatherTypeMETAR:
			if result.Err != nil {
				newData.FetchErrors = append(newData.FetchErrors, fmt.Sprintf("METAR: %s", result.Err.Error()))
				c.logger.Warn("获取 METAR 数据失败",
					logger.String("airport", airportCode),
					logger.Error(result.Err))
			} else {
				newData.METAR = result.Data
				c.logger.Debug("METAR 数据已更新",
					logger.String("airport", airportCode))
			}

		case WeatherTypeTAF:
			if result.Err != nil {
				newData.FetchErrors = append(newData.FetchErrors, fmt.Sprintf("TAF: %s", result.Err.Error()))
				c.logger.Warn("获取 TAF 数据失败",
					logger.String("airport", airportCode),
					logger.Error(result.Err))
			} else {
				newData.TAF = result.Data
				c.logger.Debug("TAF 数据已更新",
					logger.String("airport", airportCode))
			}

		case WeatherTypeNOTAMs:
			if result.Err != nil {
				newData.FetchErrors = append(newData.FetchErrors, fmt.Sprintf("NOTAMs: %s", result.Err.Error()))
				c.logger.Warn("获取 NOTAM 数据失败",
					logger.String("airport", airportCode),
					logger.Error(result.Err))
			} else {
				newData.NOTAMs = result.Data
				c.logger.Debug("NOTAM 数据已更新",
					logger.String("airport", airportCode))
			}
		}
	}

	// 用新数据更新缓存
	expiryDuration := time.Duration(c.config.CacheExpiryMinutes) * time.Minute
	c.cache.Set(newData, expiryDuration)

	// 记录缓存更新
	successCount := len(results) - len(newData.FetchErrors)
	c.logger.Info("气象缓存已更新",
		logger.String("airport", airportCode),
		logger.Int("successful_fetches", successCount),
		logger.Int("failed_fetches", len(newData.FetchErrors)),
		logger.Time("expires_at", time.Now().Add(expiryDuration)))
}

// Invalidate 清空缓存
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = NewWeatherCache()
	c.logger.Info("气象缓存已失效")
}

// GetStats 返回缓存统计信息
func (c *Cache) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data := c.cache.Get()
	stats := map[string]interface{}{
		"has_data":     data != nil,
		"is_expired":   c.cache.IsExpired(),
		"error_count":  0,
		"last_updated": time.Time{},
	}

	if data != nil {
		stats["error_count"] = len(data.FetchErrors)
		stats["last_updated"] = data.LastUpdated
		stats["has_metar"] = data.METAR != nil
		stats["has_taf"] = data.TAF != nil
		stats["has_notams"] = data.NOTAMs != nil
	}

	return stats
}
