package atcchat

import (
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/weather"
)

// ChatSession 表示一个活跃的 ATC 聊天会话
type ChatSession struct {
	ID              string    `json:"id"`
	OpenAISessionID string    `json:"openai_session_id"`
	ClientSecret    string    `json:"client_secret"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Active          bool      `json:"active"`
	LastActivity    time.Time `json:"last_activity"`
}

// ChatMessage 表示聊天会话中的一条消息
type ChatMessage struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Type      string    `json:"type"` // "user"、"assistant"、"system"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	AudioData []byte    `json:"audio_data,omitempty"`
}

// AirspaceContext 表示用于 AI 上下文的当前空域数据
type AirspaceContext struct {
	Timestamp            time.Time              `json:"timestamp"`
	Airport              AirportInfo            `json:"airport"`
	Aircraft             []*adsb.Aircraft       `json:"aircraft"`
	Weather              *weather.WeatherData   `json:"weather"`
	Runways              []RunwayInfo           `json:"runways"`
	RecentCommunications []TranscriptionSummary `json:"recent_communications"`
	ActiveSessions       int                    `json:"active_sessions"`
}

// AirportInfo 表示机场信息
type AirportInfo struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Coordinates []float64 `json:"coordinates"`
	ElevationFt int       `json:"elevation_ft"`
}

// RunwayInfo 表示跑道信息
type RunwayInfo struct {
	Name       string   `json:"name"`
	Heading    int      `json:"heading"`
	LengthFt   int      `json:"length_ft"`
	Active     bool     `json:"active"`
	Operations []string `json:"operations"`
}

// TranscriptionSummary 表示近期的无线电通信
type TranscriptionSummary struct {
	Timestamp time.Time `json:"timestamp"`
	Frequency string    `json:"frequency"`
	Content   string    `json:"content"`
	Speaker   string    `json:"speaker"`
	Callsign  string    `json:"callsign,omitempty"`
}

// PromptData 表示用于模板渲染的数据
type PromptData struct {
	Aircraft             string `json:"aircraft"`
	Weather              string `json:"weather"`
	Runways              string `json:"runways"`
	TranscriptionHistory string `json:"transcription_history"`
	Timestamp            string `json:"timestamp"`
	Airport              string `json:"airport"`
	Time                 string `json:"time"`
}

// SessionConfig 表示聊天会话的配置
type SessionConfig struct {
	InputAudioFormat  string  `json:"input_audio_format"`
	OutputAudioFormat string  `json:"output_audio_format"`
	SampleRate        int     `json:"sample_rate"`
	Channels          int     `json:"channels"`
	MaxResponseTokens int     `json:"max_response_tokens"`
	Temperature       float64 `json:"temperature"`
	TurnDetectionType string  `json:"turn_detection_type"`
	VADThreshold      float64 `json:"vad_threshold"`
	SilenceDurationMs int     `json:"silence_duration_ms"`
	Voice             string  `json:"voice"`
	Model             string  `json:"model"`
}

// WebSocketMessage 表示通过 WebSocket 发送的消息
type WebSocketMessage struct {
	Type      string                 `json:"type"`
	SessionID string                 `json:"session_id,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// AudioChunk 表示音频数据分块
type AudioChunk struct {
	SessionID string    `json:"session_id"`
	Data      []byte    `json:"data"`
	Format    string    `json:"format"`
	Timestamp time.Time `json:"timestamp"`
}

// SessionStatus 表示聊天会话的状态
type SessionStatus struct {
	ID           string    `json:"id"`
	Active       bool      `json:"active"`
	Connected    bool      `json:"connected"`
	LastActivity time.Time `json:"last_activity"`
	ExpiresAt    time.Time `json:"expires_at"`
	Error        string    `json:"error,omitempty"`
}
