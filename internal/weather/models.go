package weather

import (
	"sync"
	"time"
)

// WeatherData 表示某个机场的完整气象信息
type WeatherData struct {
	METAR       interface{} `json:"metar,omitempty"`
	TAF         interface{} `json:"taf,omitempty"`
	NOTAMs      interface{} `json:"notams,omitempty"`
	LastUpdated time.Time   `json:"last_updated"`
	FetchErrors []string    `json:"fetch_errors,omitempty"`
}

// WeatherCache 表示带过期时间的缓存气象数据
type WeatherCache struct {
	Data      *WeatherData
	ExpiresAt time.Time
	mu        sync.RWMutex
}

// WeatherConfig 表示气象服务配置
type WeatherConfig struct {
	RefreshIntervalMinutes int    `toml:"refresh_interval_minutes"`
	APIBaseURL             string `toml:"api_base_url"`
	RequestTimeoutSeconds  int    `toml:"request_timeout_seconds"`
	MaxRetries             int    `toml:"max_retries"`
	FetchMETAR             bool   `toml:"fetch_metar"`
	FetchTAF               bool   `toml:"fetch_taf"`
	FetchNOTAMs            bool   `toml:"fetch_notams"`
	CacheExpiryMinutes     int    `toml:"cache_expiry_minutes"`
}

// WeatherType 表示气象数据的类型
type WeatherType string

const (
	WeatherTypeMETAR  WeatherType = "metar"
	WeatherTypeTAF    WeatherType = "taf"
	WeatherTypeNOTAMs WeatherType = "notams"
)

// FetchResult 表示获取气象数据的结果
type FetchResult struct {
	Type WeatherType
	Data interface{}
	Err  error
}

// IsExpired 检查缓存的数据是否已过期
func (wc *WeatherCache) IsExpired() bool {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	return time.Now().After(wc.ExpiresAt)
}

// Get 返回缓存的气象数据(线程安全)
func (wc *WeatherCache) Get() *WeatherData {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	return wc.Data
}

// Set 更新缓存的气象数据(线程安全)
func (wc *WeatherCache) Set(data *WeatherData, expiryDuration time.Duration) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.Data = data
	wc.ExpiresAt = time.Now().Add(expiryDuration)
}

// NewWeatherCache 创建一个新的气象缓存实例
func NewWeatherCache() *WeatherCache {
	return &WeatherCache{
		Data: nil, // 以无数据(而非空数据)开始
	}
}

// DefaultWeatherConfig 返回默认的气象配置
func DefaultWeatherConfig() WeatherConfig {
	return WeatherConfig{
		RefreshIntervalMinutes: 10,
		APIBaseURL:             "https://node.windy.com/airports",
		RequestTimeoutSeconds:  10,
		MaxRetries:             2,
		FetchMETAR:             true,
		FetchTAF:               true,
		FetchNOTAMs:            true,
		CacheExpiryMinutes:     15,
	}
}

// ConfigWeatherConfig 表示 config 包中的 WeatherConfig
// 用于避免循环导入
type ConfigWeatherConfig struct {
	RefreshIntervalMinutes int    `toml:"refresh_interval_minutes"`
	APIBaseURL             string `toml:"api_base_url"`
	RequestTimeoutSeconds  int    `toml:"request_timeout_seconds"`
	MaxRetries             int    `toml:"max_retries"`
	FetchMETAR             bool   `toml:"fetch_metar"`
	FetchTAF               bool   `toml:"fetch_taf"`
	FetchNOTAMs            bool   `toml:"fetch_notams"`
	CacheExpiryMinutes     int    `toml:"cache_expiry_minutes"`
}

// FromConfigWeatherConfig 将 config.WeatherConfig 转换为 weather.WeatherConfig
func FromConfigWeatherConfig(cfg ConfigWeatherConfig) WeatherConfig {
	return WeatherConfig{
		RefreshIntervalMinutes: cfg.RefreshIntervalMinutes,
		APIBaseURL:             cfg.APIBaseURL,
		RequestTimeoutSeconds:  cfg.RequestTimeoutSeconds,
		MaxRetries:             cfg.MaxRetries,
		FetchMETAR:             cfg.FetchMETAR,
		FetchTAF:               cfg.FetchTAF,
		FetchNOTAMs:            cfg.FetchNOTAMs,
		CacheExpiryMinutes:     cfg.CacheExpiryMinutes,
	}
}
