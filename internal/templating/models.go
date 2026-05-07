package templating

import (
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/weather"
)

// TemplateContext 表示用于模板渲染的原始数据上下文
type TemplateContext struct {
	Aircraft             []*adsb.Aircraft       `json:"aircraft"`
	Weather              *weather.WeatherData   `json:"weather"`
	Runways              []RunwayInfo           `json:"runways"`
	ActiveRunways        []adsb.RunwayScore     `json:"active_runways"`
	TranscriptionHistory []TranscriptionSummary `json:"transcription_history"`
	Airport              AirportInfo            `json:"airport"`
	Timestamp            time.Time              `json:"timestamp"`
}

// TemplateData 表示用于模板渲染的已格式化数据
type TemplateData struct {
	Aircraft             string    `json:"aircraft"`
	Weather              string    `json:"weather"`
	Runways              string    `json:"runways"`
	ActiveRunways        string    `json:"active_runways"`
	TranscriptionHistory string    `json:"transcription_history"` // 仅 ATC 聊天填充
	Airport              string    `json:"airport"`
	Time                 string    `json:"time"`
	Timestamp            time.Time `json:"timestamp"`
}

// FormattingOptions 控制包含哪些数据以及如何格式化
type FormattingOptions struct {
	MaxAircraft                 int    `json:"max_aircraft"`
	IncludeWeather              bool   `json:"include_weather"`
	IncludeRunways              bool   `json:"include_runways"`
	IncludeTranscriptionHistory bool   `json:"include_transcription_history"` // 仅用于 ATC 聊天
	TimeFormat                  string `json:"time_format"`
}

// AirportInfo 表示用于模板的机场信息
type AirportInfo struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Coordinates []float64 `json:"coordinates"`
	ElevationFt int       `json:"elevation_ft"`
}

// RunwayInfo 表示用于模板的跑道信息
type RunwayInfo struct {
	Name       string   `json:"name"`
	Heading    int      `json:"heading"`
	LengthFt   int      `json:"length_ft"`
	Active     bool     `json:"active"`
	Operations []string `json:"operations"`
}

// TranscriptionSummary 表示用于模板的最近无线电通信
type TranscriptionSummary struct {
	Timestamp time.Time `json:"timestamp"`
	Frequency string    `json:"frequency"`
	Content   string    `json:"content"`
	Speaker   string    `json:"speaker"`
	Callsign  string    `json:"callsign,omitempty"`
}

// DefaultFormattingOptions 返回模板格式化的合理默认值
func DefaultFormattingOptions() FormattingOptions {
	return FormattingOptions{
		MaxAircraft:                 50,
		IncludeWeather:              true,
		IncludeRunways:              true,
		IncludeTranscriptionHistory: false, // 默认为 false,仅为 ATC 聊天显式启用
		TimeFormat:                  "Monday, January 2, 2006 at 15:04:05 UTC",
	}
}

// ATCChatFormattingOptions 返回针对 ATC 聊天优化的格式化选项
func ATCChatFormattingOptions() FormattingOptions {
	opts := DefaultFormattingOptions()
	opts.IncludeTranscriptionHistory = true
	opts.MaxAircraft = 200 // 使用较高的默认值,会被配置覆盖
	return opts
}

// PostProcessorFormattingOptions 返回针对后处理器优化的格式化选项
func PostProcessorFormattingOptions() FormattingOptions {
	opts := DefaultFormattingOptions()
	opts.IncludeTranscriptionHistory = false // 后处理器在用户输入中获取转写
	opts.MaxAircraft = 100
	return opts
}
