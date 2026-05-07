package adsb

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// FlexibleField 可以保存字符串或数字
type FlexibleField struct {
	value interface{}
}

func (f *FlexibleField) HasValue() bool {
	if f == nil || f.value == nil {
		return false
	}
	if s, ok := f.value.(string); ok {
		return s != ""
	}
	return true
}

// UnmarshalJSON 为 FlexibleField 实现自定义 JSON 反序列化
func (f *FlexibleField) UnmarshalJSON(data []byte) error {
	// 首先尝试反序列化为数字
	var num float64
	if err := json.Unmarshal(data, &num); err == nil {
		f.value = num
		return nil
	}

	// 如果失败,尝试反序列化为字符串
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		f.value = str
		return nil
	}

	// 如果都失败,尝试反序列化为布尔值
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		f.value = b
		return nil
	}

	// 如果都失败,返回错误
	return fmt.Errorf("无法将 %s 反序列化为 FlexibleField", data)
}

// Float64 将值作为 float64 返回
func (f *FlexibleField) Float64() float64 {
	v, ok := f.Float64OK()
	if !ok {
		return 0
	}
	return v
}

func (f *FlexibleField) Float64OK() (float64, bool) {
	switch v := f.value.(type) {
	case float64:
		return v, true
	case string:
		if v == "" {
			return 0, false
		}
		if v == "ground" {
			return 0, true
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func (f *FlexibleField) Float64Ptr() *float64 {
	if !f.HasValue() {
		return nil
	}
	v, ok := f.Float64OK()
	if !ok {
		return nil
	}
	return &v
}

// Int 将值作为 int 返回
func (f *FlexibleField) Int() int {
	v, ok := f.IntOK()
	if !ok {
		return 0
	}
	return v
}

func (f *FlexibleField) IntOK() (int, bool) {
	if !f.HasValue() {
		return 0, false
	}

	switch v := f.value.(type) {
	case float64:
		return int(v), true
	case string:
		if v == "" {
			return 0, false
		}
		i, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return i, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func (f *FlexibleField) IntPtr() *int {
	v, ok := f.IntOK()
	if !ok {
		return nil
	}
	return &v
}

// String 将值作为字符串返回
func (f *FlexibleField) String() string {
	switch v := f.value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

// ExternalADSBTarget 表示外部 ADS-B API 响应中的单个飞行器
// 它使用 FlexibleField 处理可以是字符串或数字的字段
type ExternalADSBTarget struct {
	Hex            string        `json:"hex"`
	Type           string        `json:"type"`
	Flight         string        `json:"flight"`
	Registration   string        `json:"r"` // 外部 API 特定字段
	AircraftType   string        `json:"t"` // 外部 API 特定字段
	AltBaro        FlexibleField `json:"alt_baro"`
	AltGeom        FlexibleField `json:"alt_geom"`
	GS             FlexibleField `json:"gs"`
	IAS            FlexibleField `json:"ias"`
	TAS            FlexibleField `json:"tas"`
	Mach           FlexibleField `json:"mach"`
	WD             FlexibleField `json:"wd"`
	WS             FlexibleField `json:"ws"`
	OAT            FlexibleField `json:"oat"`
	TAT            FlexibleField `json:"tat"`
	Track          FlexibleField `json:"track"`
	TrackRate      FlexibleField `json:"track_rate"`
	Roll           FlexibleField `json:"roll"`
	MagHeading     FlexibleField `json:"mag_heading"`
	TrueHeading    FlexibleField `json:"true_heading"`
	BaroRate       FlexibleField `json:"baro_rate"`
	GeomRate       FlexibleField `json:"geom_rate"`
	Squawk         string        `json:"squawk"`
	Category       string        `json:"category"`
	NavQNH         FlexibleField `json:"nav_qnh"`
	NavAltitudeMCP FlexibleField `json:"nav_altitude_mcp"`
	NavAltitudeFMS FlexibleField `json:"nav_altitude_fms"`
	NavHeading     FlexibleField `json:"nav_heading"`
	Lat            FlexibleField `json:"lat"`
	Lon            FlexibleField `json:"lon"`
	NIC            FlexibleField `json:"nic"`
	RC             FlexibleField `json:"rc"`
	SeenPos        FlexibleField `json:"seen_pos"`
	RDst           FlexibleField `json:"r_dst"`
	RDir           FlexibleField `json:"r_dir"`
	Version        FlexibleField `json:"version"`
	NICBaro        FlexibleField `json:"nic_baro"`
	NACP           FlexibleField `json:"nac_p"`
	NACV           FlexibleField `json:"nac_v"`
	SIL            FlexibleField `json:"sil"`
	SILType        string        `json:"sil_type"`
	GVA            FlexibleField `json:"gva"`
	SDA            FlexibleField `json:"sda"`
	Alert          FlexibleField `json:"alert"`
	SPI            FlexibleField `json:"spi"`
	MLAT           []string      `json:"mlat"`
	TISB           []string      `json:"tisb"`
	Messages       FlexibleField `json:"messages"`
	Seen           FlexibleField `json:"seen"`
	RSSI           FlexibleField `json:"rssi"`
}

// ExternalAPIResponse 表示来自外部 ADS-B API 的原始 JSON 数据
type ExternalAPIResponse struct {
	Now      float64              `json:"now,omitempty"`
	Messages int                  `json:"messages,omitempty"`
	AC       []ExternalADSBTarget `json:"ac"`
}

// Convert 将 ExternalADSBTarget 转换为标准 ADSBTarget 格式
func (e *ExternalADSBTarget) Convert() ADSBTarget {
	target := ADSBTarget{
		Hex:          e.Hex,
		Type:         e.Type,
		Flight:       e.Flight,
		Registration: e.Registration, // 复制注册字段
		AircraftType: e.AircraftType, // 复制飞行器类型字段
		Squawk:       e.Squawk,
		Category:     e.Category,
		SILType:      e.SILType,
		MLAT:         e.MLAT,
		TISB:         e.TISB,
		SourceType:   SourceTypeExternalAPI, // 标记为来自外部数据源
	}

	// 转换数字字段
	if v, ok := e.AltBaro.Float64OK(); ok {
		target.AltBaro = FlexibleFloat64(v)
	} else {
		target.AltBaro = NullFlexibleFloat64()
	}
	if v, ok := e.AltGeom.Float64OK(); ok {
		target.AltGeom = FlexibleFloat64(v)
	} else {
		target.AltGeom = NullFlexibleFloat64()
	}
	target.GS = e.GS.Float64Ptr()
	target.IAS = e.IAS.Float64Ptr()
	target.TAS = e.TAS.Float64Ptr()
	target.Mach = e.Mach.Float64Ptr()
	target.WD = e.WD.Float64Ptr()
	target.WS = e.WS.Float64Ptr()
	target.OAT = e.OAT.Float64Ptr()
	target.TAT = e.TAT.Float64Ptr()
	target.Track = e.Track.Float64Ptr()
	target.TrackRate = e.TrackRate.Float64Ptr()
	target.Roll = e.Roll.Float64Ptr()
	target.MagHeading = e.MagHeading.Float64Ptr()
	target.TrueHeading = e.TrueHeading.Float64Ptr()
	target.BaroRate = e.BaroRate.Float64Ptr()
	target.GeomRate = e.GeomRate.Float64Ptr()
	target.NavQNH = e.NavQNH.Float64Ptr()
	target.NavAltitudeMCP = e.NavAltitudeMCP.Float64Ptr()
	target.NavAltitudeFMS = e.NavAltitudeFMS.Float64Ptr()
	target.NavHeading = e.NavHeading.Float64Ptr()
	target.Lat = e.Lat.Float64Ptr()
	target.Lon = e.Lon.Float64Ptr()
	target.SeenPos = e.SeenPos.Float64Ptr()
	target.RDst = e.RDst.Float64Ptr()
	target.RDir = e.RDir.Float64Ptr()
	target.RSSI = e.RSSI.Float64Ptr()
	target.Seen = e.Seen.Float64Ptr()

	// 转换整数字段
	target.NIC = e.NIC.IntPtr()
	target.RC = e.RC.IntPtr()
	target.Version = e.Version.IntPtr()
	target.NICBaro = e.NICBaro.IntPtr()
	target.NACP = e.NACP.IntPtr()
	target.NACV = e.NACV.IntPtr()
	target.SIL = e.SIL.IntPtr()
	target.GVA = e.GVA.IntPtr()
	target.SDA = e.SDA.IntPtr()
	target.Alert = e.Alert.IntPtr()
	target.SPI = e.SPI.IntPtr()
	target.Messages = e.Messages.IntPtr()

	return target
}
