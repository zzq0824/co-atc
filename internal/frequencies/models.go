package frequencies

import (
	"context"
	"io"
	"time"
)

// Frequency 表示一个被监控的 ATC 频率
type Frequency struct {
	ID              string    `json:"id"`
	Airport         string    `json:"airport"`
	Name            string    `json:"name"`
	FrequencyMHz    float64   `json:"frequency_mhz"`
	URL             string    `json:"url"`
	Status          string    `json:"status"` // "active"、"connecting"、"error"
	LastError       string    `json:"last_error,omitempty"`
	Bitrate         int       `json:"bitrate,omitempty"`
	Format          string    `json:"format,omitempty"`
	StreamURL       string    `json:"stream_url"`  // 从本服务器流式传输的相对 URL 路径
	StreamPort      int       `json:"stream_port"` // 用于流式传输的端口(用于负载分发)
	LastActive      time.Time `json:"last_active,omitempty"`
	Order           int       `json:"order"`            // 用于显示/排序的顺序
	TranscribeAudio bool      `json:"transcribe_audio"` // 是否对该频率的音频进行转写
}

// Stream 表示单个活跃客户端连接到音频源所占用的资源。
// 在该模型中它不是共享资源。
type Stream struct {
	Reader         io.ReadCloser // 客户端的专用 reader(现在来自 audio.Processor)
	OriginalStream io.ReadCloser // 客户端到音频源的专用连接(为向后兼容保留)
	ContentType    string
	Bitrate        int                // 流的元数据
	Format         string             // 流的元数据
	processCancel  context.CancelFunc // 用于取消负责将该客户端的 OriginalStream 拷贝到 Reader 的 goroutine
}

// FrequencyResponse 表示频率数据的 API 响应
type FrequencyResponse struct {
	Timestamp   time.Time   `json:"timestamp"`
	Count       int         `json:"count"`
	Frequencies []Frequency `json:"frequencies"`
}

// StreamOptions 包含流式传输的选项
type StreamOptions struct {
	URL        string
	BufferSize int
	Timeout    time.Duration
}

// StreamMetadata 包含音频流的元数据
type StreamMetadata struct {
	ContentType string
	Bitrate     int
	Format      string
	Description string
	Genre       string
	Name        string
}
