package adsb

import "time"

// 数据源类型常量
const (
	SourceTypeExternalAPI     = "external-rapidapi"
	SourceTypeExternalOpenSky = "external-opensky"
	SourceTypeTar1090         = "tar1090"
	SourceTypeReadsbAPI       = "readsb-api"
	SourceTypeReadsbFile      = "readsb-file"
)

// SourceChannelStatus 描述某一数据通道(飞行器/接收机/统计)的可用性与最近状态
type SourceChannelStatus struct {
	Available     bool       `json:"available"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Data          any        `json:"data"`
}

// SourceStatus 描述当前 ADS-B 数据源的整体状态,供 API/前端展示
type SourceStatus struct {
	SourceType string              `json:"source_type"`
	Mode       string              `json:"mode"`
	Status     string              `json:"status"`
	Aircraft   SourceChannelStatus `json:"aircraft"`
	Receiver   SourceChannelStatus `json:"receiver"`
	Stats      SourceChannelStatus `json:"stats"`
	UpdatedAt  *time.Time          `json:"updated_at,omitempty"`
}
