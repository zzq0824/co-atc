package weather

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// Client 处理对气象 API 的 HTTP 请求
type Client struct {
	config     WeatherConfig
	httpClient *http.Client
	logger     *logger.Logger
}

// NewClient 创建一个新的气象 API 客户端
func NewClient(config WeatherConfig, logger *logger.Logger) *Client {
	return &Client{
		config: config,
		httpClient: &http.Client{
			Timeout: time.Duration(config.RequestTimeoutSeconds) * time.Second,
		},
		logger: logger.Named("weather-client"),
	}
}

// FetchMETAR 获取指定机场的 METAR 数据
func (c *Client) FetchMETAR(airportCode string) (interface{}, error) {
	url := fmt.Sprintf("%s/metar/%s", c.config.APIBaseURL, airportCode)
	return c.fetchWithRetry(url, WeatherTypeMETAR, airportCode)
}

// FetchTAF 获取指定机场的 TAF 数据
func (c *Client) FetchTAF(airportCode string) (interface{}, error) {
	url := fmt.Sprintf("%s/taf/%s", c.config.APIBaseURL, airportCode)
	return c.fetchWithRetry(url, WeatherTypeTAF, airportCode)
}

// FetchNOTAMs 获取指定机场的 NOTAM 数据
func (c *Client) FetchNOTAMs(airportCode string) (interface{}, error) {
	url := fmt.Sprintf("%s/notams/%s", c.config.APIBaseURL, airportCode)
	return c.fetchWithRetry(url, WeatherTypeNOTAMs, airportCode)
}

// fetchWithRetry 执行带重试逻辑和指数退避的 HTTP 请求
func (c *Client) fetchWithRetry(url string, weatherType WeatherType, airportCode string) (interface{}, error) {
	var lastErr error
	var data interface{}

	// 尝试带重试地获取
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// 重试之间使用指数退避
			backoffDuration := time.Duration(500*(1<<uint(attempt-1))) * time.Millisecond
			c.logger.Info("正在重试获取气象数据",
				logger.String("type", string(weatherType)),
				logger.String("airport", airportCode),
				logger.Int("attempt", attempt),
				logger.String("backoff", backoffDuration.String()))
			time.Sleep(backoffDuration)
		}

		// 发起请求
		resp, err := c.httpClient.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("向气象 API 发起请求时出错: %w", err)
			c.logger.Warn("气象 API 请求失败,可能会重试",
				logger.String("type", string(weatherType)),
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", c.config.MaxRetries+1))
			continue
		}

		// 确保响应体被关闭
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("状态码异常: %d", resp.StatusCode)
			c.logger.Warn("气象 API 返回非 OK 状态,可能会重试",
				logger.String("type", string(weatherType)),
				logger.String("airport", airportCode),
				logger.Int("status_code", resp.StatusCode),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", c.config.MaxRetries+1))
			continue
		}

		// 读取并解析响应
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			lastErr = fmt.Errorf("解码气象数据时出错: %w", err)
			c.logger.Warn("解码气象数据失败,可能会重试",
				logger.String("type", string(weatherType)),
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", c.config.MaxRetries+1))
			continue
		}

		// 成功 - 返回数据
		if attempt > 0 {
			c.logger.Info("经过重试后成功获取气象数据",
				logger.String("type", string(weatherType)),
				logger.String("airport", airportCode),
				logger.Int("attempts_needed", attempt+1))
		}
		return data, nil
	}

	// 如果到这里,说明所有尝试都失败了
	c.logger.Error("获取气象数据的所有尝试均失败",
		logger.String("type", string(weatherType)),
		logger.String("airport", airportCode),
		logger.Error(lastErr),
		logger.Int("max_attempts", c.config.MaxRetries+1))
	return nil, lastErr
}

// FetchAll 并发获取所有已启用的气象数据类型
func (c *Client) FetchAll(airportCode string) []FetchResult {
	results := make(chan FetchResult, 3)
	var fetchCount int

	// 为已启用的气象类型启动并发获取
	if c.config.FetchMETAR {
		fetchCount++
		go func() {
			data, err := c.FetchMETAR(airportCode)
			results <- FetchResult{Type: WeatherTypeMETAR, Data: data, Err: err}
		}()
	}

	if c.config.FetchTAF {
		fetchCount++
		go func() {
			data, err := c.FetchTAF(airportCode)
			results <- FetchResult{Type: WeatherTypeTAF, Data: data, Err: err}
		}()
	}

	if c.config.FetchNOTAMs {
		fetchCount++
		go func() {
			data, err := c.FetchNOTAMs(airportCode)
			results <- FetchResult{Type: WeatherTypeNOTAMs, Data: data, Err: err}
		}()
	}

	// 收集结果
	var fetchResults []FetchResult
	for i := 0; i < fetchCount; i++ {
		result := <-results
		fetchResults = append(fetchResults, result)
	}

	return fetchResults
}
