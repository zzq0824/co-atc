package sqlite

import "time"

// ClearanceRecord 表示从转写中提取的许可
type ClearanceRecord struct {
	ID              int64     `json:"id"`
	TranscriptionID int64     `json:"transcription_id"`
	Callsign        string    `json:"callsign"`
	ClearanceType   string    `json:"clearance_type"` // "takeoff" 或 "landing"
	ClearanceText   string    `json:"clearance_text"`
	Runway          string    `json:"runway,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	Status          string    `json:"status"` // "issued", "complied", "deviation"
	CreatedAt       time.Time `json:"created_at"`
}

// ExtractedClearance 表示来自 AI 处理的许可数据
type ExtractedClearance struct {
	Callsign string `json:"callsign"`
	Type     string `json:"type"` // "takeoff" 或 "landing"
	Text     string `json:"text"` // 完整许可文本
	Runway   string `json:"runway,omitempty"`
}
