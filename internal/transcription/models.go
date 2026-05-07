package transcription

import (
	"time"
)

// TranscriptionEvent 表示一个转写事件
type TranscriptionEvent struct {
	Type      string    // "delta" 或 "completed"
	Text      string    // 转写文本
	Timestamp time.Time // 事件发生的时间
}

// Config 表示转写服务的配置
type Config struct {
	OpenAIAPIKey          string
	Model                 string
	Language              string
	NoiseReduction        string
	ChunkMs               int
	BufferSizeKB          int
	FFmpegPath            string
	FFmpegSampleRate      int
	FFmpegChannels        int
	FFmpegFormat          string
	ReconnectIntervalSec  int
	MaxRetries            int
	TurnDetectionType     string
	PrefixPaddingMs       int
	SilenceDurationMs     int
	VADThreshold          float64
	RetryMaxAttempts      int
	RetryInitialBackoffMs int
	RetryMaxBackoffMs     int
	PromptPath            string
	Prompt                string // 从 PromptPath 加载
	TimeoutSeconds        int    // OpenAI API 请求的 HTTP 超时
	LogDir                string // 转写日志文件的可选目录
}
