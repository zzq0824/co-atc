package adsb

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// FlexibleFloat64 是一个 float64,可以从数字值和
// 像 "ground" 这样的字符串(在飞行器在地面时出现在 ADS-B 数据中)反序列化
type FlexibleFloat64 float64

func NullFlexibleFloat64() FlexibleFloat64 {
	return FlexibleFloat64(math.NaN())
}

func (f FlexibleFloat64) IsSet() bool {
	return !math.IsNaN(float64(f))
}

func (f FlexibleFloat64) MarshalJSON() ([]byte, error) {
	if !f.IsSet() {
		return []byte("null"), nil
	}
	return json.Marshal(float64(f))
}

// UnmarshalJSON 为 FlexibleFloat64 实现 json.Unmarshaler
func (f *FlexibleFloat64) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = NullFlexibleFloat64()
		return nil
	}

	// 首先尝试反序列化为 float64
	var floatVal float64
	if err := json.Unmarshal(data, &floatVal); err == nil {
		*f = FlexibleFloat64(floatVal)
		return nil
	}

	// 如果失败,尝试作为字符串(处理 "ground" 情况)
	var strVal string
	if err := json.Unmarshal(data, &strVal); err == nil {
		s := strings.TrimSpace(strVal)
		if s == "" {
			*f = NullFlexibleFloat64()
			return nil
		}
		if strings.EqualFold(s, "ground") {
			*f = 0
			return nil
		}

		if parsed, parseErr := strconv.ParseFloat(s, 64); parseErr == nil {
			*f = FlexibleFloat64(parsed)
			return nil
		}

		*f = NullFlexibleFloat64()
		return nil
	}

	// 如果都失败,保留缺失为 null 等效值
	*f = NullFlexibleFloat64()
	return nil
}

// Float64 将值作为标准 float64 返回
func (f FlexibleFloat64) Float64() float64 {
	if !f.IsSet() {
		return 0
	}
	return float64(f)
}

func (f FlexibleFloat64) NullableValue() interface{} {
	if !f.IsSet() {
		return nil
	}
	return float64(f)
}

// RawAircraftData 表示来自 ADS-B 数据源的原始 JSON 数据
type RawAircraftData struct {
	Now      float64      `json:"now"`
	Messages int          `json:"messages"`
	Aircraft []ADSBTarget `json:"aircraft"`
}

// ADSBTarget 表示原始 ADS-B 数据中的单个飞行器
// 这对应于 adsb_targets 表中的条目
type ADSBTarget struct {
	Hex              string             `json:"hex"`
	Type             string             `json:"type"`
	Flight           string             `json:"flight"`
	Registration     string             `json:"r,omitempty"` // 外部 API 特定字段(r)
	AircraftType     string             `json:"t,omitempty"` // 外部 API 特定字段(t)
	AltBaro          FlexibleFloat64    `json:"alt_baro"`    // 可以是 "ground" 字符串或数字
	AltGeom          FlexibleFloat64    `json:"alt_geom"`    // 可以是 "ground" 字符串或数字
	GS               *float64           `json:"gs"`
	IAS              *float64           `json:"ias"`
	TAS              *float64           `json:"tas"`
	Mach             *float64           `json:"mach"`
	WD               *float64           `json:"wd"`
	WS               *float64           `json:"ws"`
	OAT              *float64           `json:"oat"`
	TAT              *float64           `json:"tat"`
	Track            *float64           `json:"track"`
	TrackRate        *float64           `json:"track_rate"`
	Roll             *float64           `json:"roll"`
	MagHeading       *float64           `json:"mag_heading"`
	TrueHeading      *float64           `json:"true_heading"`
	BaroRate         *float64           `json:"baro_rate"`
	GeomRate         *float64           `json:"geom_rate"`
	Squawk           string             `json:"squawk"`
	Category         string             `json:"category"`
	NavQNH           *float64           `json:"nav_qnh"`
	NavAltitudeMCP   *float64           `json:"nav_altitude_mcp"`
	NavAltitudeFMS   *float64           `json:"nav_altitude_fms"`
	NavHeading       *float64           `json:"nav_heading"`
	Lat              *float64           `json:"lat"`
	Lon              *float64           `json:"lon"`
	NIC              *int               `json:"nic"`
	RC               *int               `json:"rc"`
	SeenPos          *float64           `json:"seen_pos"`
	RDst             *float64           `json:"r_dst"`
	RDir             *float64           `json:"r_dir"`
	Version          *int               `json:"version"`
	NICBaro          *int               `json:"nic_baro"`
	NACP             *int               `json:"nac_p"`
	NACV             *int               `json:"nac_v"`
	SIL              *int               `json:"sil"`
	SILType          string             `json:"sil_type"`
	GVA              *int               `json:"gva"`
	SDA              *int               `json:"sda"`
	Alert            *int               `json:"alert"`
	SPI              *int               `json:"spi"`
	MLAT             []string           `json:"mlat"`
	TISB             []string           `json:"tisb"`
	Messages         *int               `json:"messages"`
	Seen             *float64           `json:"seen"`
	RSSI             *float64           `json:"rssi"`
	OnGroundReported *bool              `json:"on_ground_reported,omitempty"`
	ATCDerived       *ATCDerivedMetrics `json:"atc_derived,omitempty"`
	SourceType       string             `json:"source_type,omitempty"` // 指示数据源模式(external-rapidapi、external-opensky、tar1090、readsb-api、readsb-file)
}

func NumberOrZero(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func NumberPtr(v float64) *float64 {
	return &v
}

func IntOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func IntPtr(v int) *int {
	return &v
}

func (a *ADSBTarget) HasPosition() bool {
	return a != nil && a.Lat != nil && a.Lon != nil
}

func (a *ADSBTarget) Position() (float64, float64, bool) {
	if !a.HasPosition() {
		return 0, 0, false
	}
	return *a.Lat, *a.Lon, true
}

// ATCDerivedMetrics 表示用于 ATC 用途的服务器端派生操作指标。
// HeadTailwindKt 符号:+ 顶风,- 顺风。
// CrosswindKt 符号:+ 来自右侧,- 来自左侧。
type ATCDerivedMetrics struct {
	HeadingSource        string   `json:"heading_source,omitempty"`
	TrackHeadingErrorDeg *float64 `json:"track_heading_error_deg,omitempty"`
	HeadTailwindKt       *float64 `json:"head_tailwind_kt,omitempty"`
	CrosswindKt          *float64 `json:"crosswind_kt,omitempty"`
	FlightPathAngleDeg   *float64 `json:"flight_path_angle_deg,omitempty"`
	ClimbGradientFtNm    *float64 `json:"climb_gradient_ft_nm,omitempty"`
	TurnRateDegSec       *float64 `json:"turn_rate_deg_sec,omitempty"`
	ETAStationSec        *float64 `json:"eta_station_sec,omitempty"`
}

// PositionMinimal 表示用于地图轨迹的最小历史位置
type PositionMinimal struct {
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	AltBaro   float64   `json:"alt_baro"`
	Timestamp time.Time `json:"timestamp"`
}

// PhaseChange 表示单个阶段变化记录
type PhaseChange struct {
	ID        int       `json:"id"`
	Phase     string    `json:"phase"`
	Timestamp time.Time `json:"timestamp"`
	ADSBId    *int      `json:"adsb_id"`
}

// PhaseChangeInsert 表示要批量插入的阶段变化
type PhaseChangeInsert struct {
	Hex       string    `json:"hex"`
	Flight    string    `json:"flight"`
	Phase     string    `json:"phase"`
	Timestamp time.Time `json:"timestamp"`
	ADSBId    *int      `json:"adsb_id"`
	EventType string    `json:"event_type"` // "takeoff"、"landing",或正常阶段变化为 ""
}

// PhaseData 表示飞行器的阶段信息
type PhaseData struct {
	Current []PhaseChange `json:"current"` // 包含最新阶段的数组(与历史中的第一个项相同)
	History []PhaseChange `json:"history"` // 按时间戳降序排列的所有阶段变化
}

// BSDBData 表示来自 BaseStation.sqb 数据库的飞行器数据
type BSDBData struct {
	Registration     string `json:"registration,omitempty"`
	ICAOTypeCode     string `json:"icao_type_code,omitempty"`
	OperatorFlagCode string `json:"operator_flag_code,omitempty"`
	Manufacturer     string `json:"manufacturer,omitempty"`
	Type             string `json:"type,omitempty"`
	RegisteredOwners string `json:"registered_owners,omitempty"`
}

// Aircraft 表示具有基本字段和状态的已处理飞行器
type Aircraft struct {
	Hex                string              `json:"hex"`
	Flight             string              `json:"flight"`
	Airline            string              `json:"airline"`
	AirlineCountry     string              `json:"airline_country,omitempty"`
	Status             string              `json:"status"`
	LastSeen           time.Time           `json:"last_seen"`
	OnGround           bool                `json:"on_ground"`
	DateLanded         *time.Time          `json:"date_landed"`            // 来自 phase_changes 表 JOIN
	DateTookoff        *time.Time          `json:"date_tookoff"`           // 来自 phase_changes 表 JOIN
	CreatedAt          time.Time           `json:"created_at"`             // 首次看到飞行器的时间
	Distance           *float64            `json:"distance,omitempty"`     // 距站点的距离(海里)
	RelativeDistance   *float64            `json:"rel_distance,omitempty"` // 距参考飞行器的距离(海里)
	RelativeBearing    *float64            `json:"rel_bearing,omitempty"`  // 距参考飞行器的相对方位角(0 到 360)
	RelativeAlt        *float64            `json:"rel_altitude,omitempty"` // 距参考飞行器的相对高度(英尺)
	ADSB               *ADSBTarget         `json:"adsb,omitempty"`
	BSDB               *BSDBData           `json:"bsdb,omitempty"`                // BaseStation.sqb 增强数据
	History            []PositionMinimal   `json:"history,omitempty"`             // 用于地图轨迹的最小历史位置
	Future             []Position          `json:"future,omitempty"`              // 预测的未来位置
	Hindcast           []Position          `json:"hindcast,omitempty"`            // 首次 ADS-B 接触前预测的位置
	Phase              *PhaseData          `json:"phase,omitempty"`               // 包含当前和历史的阶段信息
	Clearances         []ClearanceData     `json:"clearances,omitempty"`          // 此飞行器的近期放行许可
	IsSimulated        bool                `json:"is_simulated"`                  // 这是否为仿真飞行器
	SimulationControls *SimulationControls `json:"simulation_controls,omitempty"` // 仿真控制参数
}

// SimulationControls 表示仿真飞行器的控制参数
type SimulationControls struct {
	TargetHeading      float64 `json:"target_heading"`       // 目标航向(度,0-359)
	TargetSpeed        float64 `json:"target_speed"`         // 目标真空速(节)
	TargetVerticalRate float64 `json:"target_vertical_rate"` // 目标垂直速率(英尺/分钟)
}

// ClearanceData 表示 API 响应中的放行许可信息
type ClearanceData struct {
	ID              int64     `json:"id"`
	Type            string    `json:"type"` // "takeoff" 或 "landing"
	Text            string    `json:"text"` // 完整放行许可文本
	Runway          string    `json:"runway,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	Status          string    `json:"status"`            // "issued"、"complied"、"deviation"
	TimeSinceIssued string    `json:"time_since_issued"` // 自发出后的可读时间
}

// Position 表示飞行器的历史位置
type Position struct {
	ID            *int      `json:"id,omitempty"` // ADSB 记录 ID
	Lat           *float64  `json:"lat"`
	Lon           *float64  `json:"lon"`
	Altitude      *float64  `json:"altitude"`
	SpeedTrue     *float64  `json:"speed_true"`
	SpeedGS       *float64  `json:"speed_gs"`
	Track         *float64  `json:"track"`
	TrueHeading   *float64  `json:"true_heading"`
	MagHeading    *float64  `json:"mag_heading"`
	VerticalSpeed *float64  `json:"vertical_speed"`
	Timestamp     time.Time `json:"timestamp"`
	Distance      *float64  `json:"distance,omitempty"`       // 距站点的距离(海里)
	SkippedBefore int       `json:"skipped_before,omitempty"` // 此位置之前隐藏的重复位置数
	SkippedAfter  int       `json:"skipped_after,omitempty"`  // 此位置之后隐藏的重复位置数(尾随)
}

// AircraftMap 是按十六进制 ID 索引的飞行器映射
type AircraftMap map[string]*Aircraft

// AircraftSimple 表示仅包含基本字段的轻量飞行器
type AircraftSimple struct {
	Hex              string   `json:"hex"`
	Callsign         string   `json:"callsign,omitempty"`
	Registration     string   `json:"registration,omitempty"`
	AircraftType     string   `json:"aircraft_type,omitempty"`
	Manufacturer     string   `json:"manufacturer,omitempty"`
	RegisteredOwners string   `json:"registered_owners,omitempty"`
	Airline          string   `json:"airline,omitempty"`
	Category         string   `json:"category,omitempty"`
	Lat              *float64 `json:"lat,omitempty"`
	Lon              *float64 `json:"lon,omitempty"`
	AltBaro          float64  `json:"alt_baro"`
	GroundSpeed      *float64 `json:"ground_speed"`
	TrueAirspeed     *float64 `json:"true_speed,omitempty"`
	Track            *float64 `json:"track"`
	MagHeading       *float64 `json:"mag_heading"`
	VerticalRate     *float64 `json:"vertical_rate"`
	Squawk           string   `json:"squawk,omitempty"`
	Distance         *float64 `json:"distance,omitempty"`
	Phase            string   `json:"phase,omitempty"`
	Status           string   `json:"status"`
}

// AircraftSimpleResponse 表示简化飞行器数据的 API 响应
type AircraftSimpleResponse struct {
	Timestamp time.Time         `json:"timestamp"`
	Count     int               `json:"count"`
	Aircraft  []*AircraftSimple `json:"aircraft"`
}

// AircraftCounts 表示按地面/空中和活动/总数计数的飞行器
type AircraftCounts struct {
	GroundActive int `json:"ground_active"`
	GroundTotal  int `json:"ground_total"`
	AirActive    int `json:"air_active"`
	AirTotal     int `json:"air_total"`
}

// AircraftResponse 表示飞行器数据的 API 响应
type AircraftResponse struct {
	Timestamp time.Time      `json:"timestamp"`
	Count     int            `json:"count"`
	Counts    AircraftCounts `json:"counts"`
	Aircraft  []*Aircraft    `json:"aircraft"`
}

// AircraftHistoryResponse 表示飞行器历史的 API 响应
type AircraftHistoryResponse struct {
	Hex      string     `json:"hex"`
	Flight   string     `json:"flight"`
	Distance *float64   `json:"distance,omitempty"` // 距站点的距离(海里)
	History  []Position `json:"history"`            // 历史位置(从 Positions 重命名)
}

// AircraftFutureResponse 表示飞行器未来预测的 API 响应
type AircraftFutureResponse struct {
	Hex      string     `json:"hex"`
	Flight   string     `json:"flight"`
	Distance *float64   `json:"distance,omitempty"` // 距站点的距离(海里)
	Future   []Position `json:"future"`             // 未来预测位置
}

// AircraftTracksResponse 表示飞行器轨迹的 API 响应(结合历史和未来)
type AircraftTracksResponse struct {
	Hex          string        `json:"hex"`
	Flight       string        `json:"flight"`
	Distance     *float64      `json:"distance,omitempty"`      // 距站点的距离(海里)
	History      []Position    `json:"history"`                 // 历史位置
	Future       []Position    `json:"future"`                  // 未来预测位置
	Hindcast     []Position    `json:"hindcast,omitempty"`      // 覆盖前预测位置
	PhaseHistory []PhaseChange `json:"phase_history,omitempty"` // 阶段变化历史(最新优先)
}

// RunwayApproachInfo 包含飞行器接近跑道的信息
type RunwayApproachInfo struct {
	RunwayID               string  `json:"runway_id"`
	DistanceToThreshold    float64 `json:"distance_to_threshold_nm"`
	DistanceFromCenterline float64 `json:"distance_from_centerline_nm"`
	HeadingAlignment       float64 `json:"heading_alignment_deg"`
	OnApproach             bool    `json:"on_approach"`
}

// RunwayDepartureInfo 包含飞行器从跑道离港的信息
type RunwayDepartureInfo struct {
	RunwayID              string  `json:"runway_id"`
	DistanceFromThreshold float64 `json:"distance_from_threshold_nm"`
	HeadingAlignment      float64 `json:"heading_alignment_deg"`
	OnDeparture           bool    `json:"on_departure"`
}

// PhaseChangeAlert 表示飞行阶段变化警报
type PhaseChangeAlert struct {
	Type      string    `json:"type"` // "phase_change"
	Hex       string    `json:"hex"`
	Flight    string    `json:"flight"`
	FromPhase string    `json:"from_phase"`
	ToPhase   string    `json:"to_phase"`
	EventType string    `json:"event_type"` // "takeoff"、"landing"、"phase_change"
	Timestamp time.Time `json:"timestamp"`
	Location  struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
		Alt float64 `json:"alt"`
	} `json:"location"`
	RunwayInfo *RunwayApproachInfo `json:"runway_info,omitempty"`
}
