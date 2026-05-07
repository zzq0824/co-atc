package frequencies

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// Client 负责从音频源获取音频流
type Client struct {
	httpClient *http.Client
	logger     *logger.Logger
}

// NewClient 创建一个用于获取音频流的新客户端
func NewClient(timeout time.Duration, logger *logger.Logger) *Client {
	// 创建启用 keep-alive 的 transport
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // 对音频流禁用压缩
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // 连接超时
			KeepAlive: 30 * time.Second, // keep-alive 周期
		}).DialContext,
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		logger: logger.Named("frequencies-client"),
	}
}

// addCacheBreaker 给 URL 添加动态的缓存破坏参数
func (c *Client) addCacheBreaker(url string) string {
	// 生成基于时间戳的缓存破坏参数,类似示例
	timestamp := time.Now().UnixNano()
	separator := "?"
	if strings.Contains(url, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%snocache=%d", url, separator, timestamp)
}

// StreamAudio 启动从音频源的音频流传输
func (c *Client) StreamAudio(ctx context.Context, opts StreamOptions) (io.ReadCloser, http.Header, error) {
	// 给 URL 添加缓存破坏参数
	urlWithCacheBreaker := c.addCacheBreaker(opts.URL)

	// 创建带 context 的新请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlWithCacheBreaker, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 为 HTTP 音频流设置必要的请求头
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive") // 保持连接以进行流式传输
	req.Header.Set("User-Agent", "Co-ATC/1.0") // 通用 User-Agent

	// 执行请求
	c.logger.Debug("正在连接音频流",
		logger.String("url", urlWithCacheBreaker),
	)

	// 创建重试机制
	maxRetries := 3
	retryDelay := 1 * time.Second

	var resp *http.Response

	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err = c.httpClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break // 成功,退出重试循环
		}

		// 出错时关闭响应体
		if resp != nil {
			resp.Body.Close()
		}

		// 如果这是最后一次尝试,返回错误
		if attempt == maxRetries-1 {
			if err != nil {
				return nil, nil, fmt.Errorf("经过 %d 次尝试后执行请求失败: %w", maxRetries, err)
			}
			return nil, nil, fmt.Errorf("经过 %d 次尝试后状态码异常: %d", maxRetries, resp.StatusCode)
		}

		// 记录重试日志
		c.logger.Warn("正在重试连接音频流",
			logger.String("url", urlWithCacheBreaker),
			logger.Int("attempt", attempt+1),
			logger.Int("max_attempts", maxRetries),
			logger.Error(err),
		)

		// 重试前等待
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(retryDelay):
			// 指数退避
			retryDelay *= 2
		}
	}

	c.logger.Debug("成功连接音频流",
		logger.String("url", urlWithCacheBreaker),
		logger.String("content_type", resp.Header.Get("Content-Type")),
	)

	// 创建带缓冲的 reader 以提高性能
	bufferedReader := bufio.NewReaderSize(resp.Body, 64*1024) // 64KB 缓冲

	// 创建一个 readCloser,关闭时会关闭原始的响应体
	readCloser := &bufferedReadCloser{
		Reader: bufferedReader,
		Closer: resp.Body,
	}

	return readCloser, resp.Header, nil
}

// bufferedReadCloser 将带缓冲的 reader 与 closer 组合在一起
type bufferedReadCloser struct {
	Reader *bufio.Reader
	Closer io.Closer
}

// Read 实现 io.Reader 接口
func (b *bufferedReadCloser) Read(p []byte) (n int, err error) {
	return b.Reader.Read(p)
}

// Close 实现 io.Closer 接口
func (b *bufferedReadCloser) Close() error {
	return b.Closer.Close()
}

// ExtractMetadata 从响应头中提取元数据
func (c *Client) ExtractMetadata(headers http.Header) StreamMetadata {
	metadata := StreamMetadata{
		ContentType: headers.Get("Content-Type"),
		Description: headers.Get("icy-description"),
		Genre:       headers.Get("icy-genre"),
		Name:        headers.Get("icy-name"),
	}

	// 提取比特率
	if bitrateStr := headers.Get("icy-br"); bitrateStr != "" {
		var bitrate int
		if _, err := fmt.Sscanf(bitrateStr, "%d", &bitrate); err == nil {
			metadata.Bitrate = bitrate
		}
	}

	// 根据 content type 设置格式
	switch metadata.ContentType {
	case "audio/mpeg":
		metadata.Format = "mp3"
	case "audio/aac":
		metadata.Format = "aac"
	case "audio/ogg":
		metadata.Format = "ogg"
	default:
		metadata.Format = "unknown"
	}

	return metadata
}
