// Package adsb 提供 ADS-B 飞行器追踪、轨迹分析与飞行阶段检测。
//
// # 基于轨迹的阶段检测
//
// TrajectoryTracker 为每个被跟踪的飞行器维护近期 ADS-B 观测的滚动窗口。
// 系统不依赖单点数据做阶段判定(在覆盖边缘很脆弱),而是分析轨迹窗口
// 来计算派生状态向量 —— 速度趋势、加速度、高度斜率和到台站的距离变化率
// —— 从而实现健壮、抗噪声的飞行阶段分类。
//
// 每架飞行器的环形缓冲区在 1 秒抓取间隔下保留约 90 秒的数据。
// 派生状态在每个抓取周期重新计算一次,使用三个分析窗口:
//
//   - 短(10s):当前速度与加速度。对真实变化响应快。
//   - 中(30s):趋势检测(爬升、下降、转弯)。对噪声做平滑。
//   - 长(整个缓冲区):高置信度决策与数据缺口检测。
//
// 关键算法:
//   - 用普通最小二乘(OLS)线性回归计算高度/速度/距离趋势
//   - 对垂直速率使用中位数滤波(抵抗异常应答机数据带来的离群尖峰)
//   - 对航向变化率使用循环差(处理 0°/360° 环绕)
//   - 数据缺口检测,避免陈旧数据污染趋势
//
// 环形缓冲区预先分配并复用槽位,稳态运行时不产生 GC 压力。
// 每架飞行器的内存开销约 18 KB,500 架约 9 MB。

package adsb

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/pkg/logger"
)

const maxLivePredictionAge = 60 * time.Second

// ─── 轨迹快照 ──────────────────────────────────────────────────────

// TrajectorySnapshot 是存放在环形缓冲区中的单条 ADS-B 观测。
// 字段是 ADSBTarget 的扁平子集,带有标准的 time.Time 时间戳和
// 有效性标志。无效快照(无位置数据)仍会存储,但会从派生状态
// 计算中排除。
type TrajectorySnapshot struct {
	Timestamp   time.Time // 该观测被摄入时的 UTC 墙钟时间
	Lat         float64   // 纬度(度)
	Lon         float64   // 经度(度)
	AltBaro     float64   // 气压高度(英尺)
	AltGeom     float64   // 几何高度(英尺)
	GS          float64   // 地速(节)
	TAS         float64   // 真空速(节)
	IAS         float64   // 指示空速(节)
	Track       float64   // 航迹角(度,0-360)
	MagHeading  float64   // 磁航向(度)
	TrueHeading float64   // 真航向(度)
	BaroRate    float64   // 气压垂直速率(英尺/分钟)
	GeomRate    float64   // 几何垂直速率(英尺/分钟)
	Roll        float64   // 滚转角(度)
	TrackRate   float64   // 航迹变化率(度/秒)
	OnGround    bool      // 飞行器是否在地面
	NavAltMCP   float64   // MCP(模式控制面板)高度设定(英尺)
	NavAltFMS   float64   // FMS(飞行管理系统)高度设定(英尺)
	Seen        float64   // 距上次 ADS-B 消息的秒数(数据时新性)
	Valid       bool      // 当关键字段缺失(无位置)时为 false
}

// TrajectorySnapshotFromADSB 把 ADSBTarget 与地面状态转换为适用于
// 环形缓冲区的 TrajectorySnapshot。当 lat 和 lon 都为零(无位置
// 数据)时,快照被标记为无效。
func TrajectorySnapshotFromADSB(adsb *ADSBTarget, onGround bool, ts time.Time) TrajectorySnapshot {
	if adsb == nil {
		return TrajectorySnapshot{Timestamp: ts, Valid: false}
	}
	lat, lon, hasPosition := adsb.Position()
	return TrajectorySnapshot{
		Timestamp:   ts,
		Lat:         lat,
		Lon:         lon,
		AltBaro:     adsb.AltBaro.Float64(),
		AltGeom:     adsb.AltGeom.Float64(),
		GS:          NumberOrZero(adsb.GS),
		TAS:         NumberOrZero(adsb.TAS),
		IAS:         NumberOrZero(adsb.IAS),
		Track:       NumberOrZero(adsb.Track),
		MagHeading:  NumberOrZero(adsb.MagHeading),
		TrueHeading: NumberOrZero(adsb.TrueHeading),
		BaroRate:    NumberOrZero(adsb.BaroRate),
		GeomRate:    NumberOrZero(adsb.GeomRate),
		Roll:        NumberOrZero(adsb.Roll),
		TrackRate:   NumberOrZero(adsb.TrackRate),
		OnGround:    onGround,
		NavAltMCP:   NumberOrZero(adsb.NavAltitudeMCP),
		NavAltFMS:   NumberOrZero(adsb.NavAltitudeFMS),
		Seen:        NumberOrZero(adsb.Seen),
		Valid:       hasPosition,
	}
}

// ─── 派生状态 ────────────────────────────────────────────────────────────

// DerivedState 保存基于轨迹窗口计算出的量。
// 当有新数据到达时(DirtyDerived 标志),每个抓取周期重新计算一次。
// 阶段判定规则使用这些派生值,而不是原始瞬时读数。
type DerivedState struct {
	// 平滑后的当前状态(短窗口中位数/均值)
	GroundSpeedKts  float64 // 平滑后的地速(节)
	VerticalRateFPM float64 // 平滑后的气压垂直速率(英尺/分钟)
	TrackDeg        float64 // 平滑后的航迹角(度)

	// 加速度(短窗口内的变化率)
	GSAccelKtsPerSec   float64 // d(GS)/dt —— 正值表示加速
	VRAccelFPMPerSec   float64 // d(VerticalRate)/dt —— 正值表示爬升加快
	TrackRateDegPerSec float64 // 航向变化率(度/秒)

	// 高度统计(中窗口)
	AltMean     float64 // 平均气压高度(英尺)
	AltMin      float64 // 最小气压高度(英尺)
	AltMax      float64 // 最大气压高度(英尺)
	AltTrendFPM float64 // 高度对时间的 OLS 回归斜率,已转换为 fpm

	// 速度统计(中窗口)
	GSMean           float64 // 平均地速(节)
	GSMin            float64 // 最小地速(节)
	GSMax            float64 // 最大地速(节)
	GSTrendKtsPerSec float64 // GS 对时间的 OLS 回归斜率(节/秒)

	// 垂直速率统计(中窗口)
	VRMean   float64 // 平均垂直速率(fpm)
	VRStdDev float64 // 垂直速率的标准差

	// 到监控台站的距离与方位
	DistToStationNM   float64 // 当前到台站的距离(海里)
	DistTrendNMPerSec float64 // 距离对时间的 OLS 回归斜率(负值表示靠近)
	BearingToStation  float64 // 当前到台站的方位(度)

	// 数据质量指标
	ValidPointCount   int     // 分析窗口中有效快照的数量
	WindowDurationSec float64 // 从最早到最新有效快照的时间跨度(秒)
	DataGapDetected   bool    // 窗口中出现任何 > 2× 抓取间隔的缺口时为 true

	// 由趋势派生的布尔标志(由 ComputeDerivedState 设置)
	IsDescending         bool // AltTrendFPM 低于下降阈值且 VRMean < 0
	IsClimbing           bool // AltTrendFPM 高于爬升阈值且 VRMean > 0
	IsLevel              bool // 中窗口内 (AltMax - AltMin) 在平飞带宽内
	IsDecelerating       bool // GSTrendKtsPerSec 低于减速阈值
	IsAccelerating       bool // GSTrendKtsPerSec 高于加速阈值
	IsTurning            bool // |TrackRateDegPerSec| 超过转弯阈值
	IsApproachingStation bool // DistTrendNMPerSec < 0(向台站靠近)

	ComputedAt time.Time // 该派生状态上次计算的时间
}

// ─── 单机轨迹 ──────────────────────────────────────────────────

// AircraftTrajectory 保存单架飞行器的环形缓冲区与派生状态。
// 环形缓冲区是固定容量的切片,满后会覆盖最旧条目,稳态运行时
// 避免内存分配。
type AircraftTrajectory struct {
	Hex          string               // ICAO 十六进制标识符
	Snapshots    []TrajectorySnapshot // 环形缓冲区(固定容量)
	WriteIdx     int                  // 下一次写入位置
	Count        int                  // 已存储的条目数(最多到容量上限)
	Derived      DerivedState         // 最新派生状态
	Prediction   TrajectoryPrediction // 后报 + 预报
	DirtyDerived bool                 // 自上次计算以来新增过快照时为 true
	LastSeen     time.Time            // 用于清理时跟踪陈旧度
}

// NewAircraftTrajectory 创建带指定容量的环形缓冲区。
func NewAircraftTrajectory(hex string, capacity int) *AircraftTrajectory {
	return &AircraftTrajectory{
		Hex:       hex,
		Snapshots: make([]TrajectorySnapshot, capacity),
	}
}

// AddSnapshot 把一条快照写入环形缓冲区,并推进写入索引。
func (at *AircraftTrajectory) AddSnapshot(snap TrajectorySnapshot) {
	at.Snapshots[at.WriteIdx] = snap
	at.WriteIdx = (at.WriteIdx + 1) % cap(at.Snapshots)
	if at.Count < cap(at.Snapshots) {
		at.Count++
	}
	at.DirtyDerived = true
	at.LastSeen = snap.Timestamp
}

// ForEachSnapshot 从最旧到最新遍历所有已存储的快照,并对每个调用 fn。
// 这样避免分配临时切片。
func (at *AircraftTrajectory) ForEachSnapshot(fn func(snap *TrajectorySnapshot)) {
	capacity := cap(at.Snapshots)
	startIdx := 0
	if at.Count == capacity {
		startIdx = at.WriteIdx // 缓冲区已满时,写入位置正好是最旧的条目
	}
	for i := 0; i < at.Count; i++ {
		idx := (startIdx + i) % capacity
		fn(&at.Snapshots[idx])
	}
}

// Latest 返回最近一次添加的快照的指针,空时返回 nil。
func (at *AircraftTrajectory) Latest() *TrajectorySnapshot {
	if at.Count == 0 {
		return nil
	}
	idx := (at.WriteIdx - 1 + cap(at.Snapshots)) % cap(at.Snapshots)
	return &at.Snapshots[idx]
}

// SnapshotsInWindow 返回最近 windowSec 秒内的有效快照,按从旧到新的
// 顺序排列。会分配切片 —— 热路径上请使用 ForEachSnapshot。
func (at *AircraftTrajectory) SnapshotsInWindow(windowSec float64) []TrajectorySnapshot {
	if at.Count == 0 {
		return nil
	}
	latest := at.Latest()
	if latest == nil {
		return nil
	}
	cutoff := latest.Timestamp.Add(-time.Duration(windowSec * float64(time.Second)))
	result := make([]TrajectorySnapshot, 0, at.Count)
	at.ForEachSnapshot(func(snap *TrajectorySnapshot) {
		if snap.Valid && !snap.Timestamp.Before(cutoff) {
			result = append(result, *snap)
		}
	})
	return result
}

// ─── 轨迹 Tracker(顶层)───────────────────────────────────────────

// TrajectoryConfig 保存轨迹系统的可调参数。
type TrajectoryConfig struct {
	BufferDurationSec    int // 保留多少秒的历史数据(默认:90)
	BufferCapacity       int // 单机最大快照数(由 时长 / 抓取间隔 + 余量 计算得到)
	FetchIntervalSec     int // ADS-B 抓取间隔,单位秒(来自 ADSBConfig)
	MinPointsForAnalysis int // 进行完整轨迹分析所需的最少有效点数(默认:5)
	StaleTimeoutSec      int // 超过此时长未见到的飞行器会被移除(默认:300)
	CleanupIntervalSec   int // 清理 goroutine 的运行频率(默认:30)

	// 派生布尔标志的阈值
	DescentVRThresholdFPM   float64 // AltTrend 低于此值 = 下降(默认:-200)
	ClimbVRThresholdFPM     float64 // AltTrend 高于此值 = 爬升(默认:200)
	LevelAltBandFt          float64 // (AltMax-AltMin) 在此范围内 = 平飞(默认:200)
	TurningRateThresholdDeg float64 // |TrackRate| 高于此值 = 转弯(默认:1.5)
	DecelerationThreshold   float64 // GSTrend 低于此值 = 减速(默认:-0.5)
	AccelerationThreshold   float64 // GSTrend 高于此值 = 加速(默认:0.5)
}

// TrajectoryTracker 管理所有被跟踪飞行器的轨迹缓冲区。
// 它对并发安全:抓取 goroutine 在同一 goroutine 中调用 Ingest 和
// DeterminePhase,后台清理 goroutine 在写锁下周期性地清除陈旧条目。
type TrajectoryTracker struct {
	mu            sync.RWMutex
	aircraft      map[string]*AircraftTrajectory
	config        TrajectoryConfig
	stationLat    float64
	stationLon    float64
	runwayData    RunwayData
	phasesConfig  *config.FlightPhasesConfig
	runwayTracker *RunwayInUseTracker
	logger        *logger.Logger
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewTrajectoryTracker 使用给定配置创建并启动 tracker。
// 清理 goroutine 在后台运行直到调用 Stop。
func NewTrajectoryTracker(
	cfg TrajectoryConfig,
	stationLat, stationLon float64,
	runwayData RunwayData,
	phasesConfig *config.FlightPhasesConfig,
	log *logger.Logger,
) *TrajectoryTracker {
	tt := &TrajectoryTracker{
		aircraft:     make(map[string]*AircraftTrajectory),
		config:       cfg,
		stationLat:   stationLat,
		stationLon:   stationLon,
		runwayData:   runwayData,
		phasesConfig: phasesConfig,
		runwayTracker: NewRunwayInUseTracker(
			phasesConfig.RunwayInUseWindowMinutes,
			phasesConfig.RunwayInUseApproachWeight,
			phasesConfig.RunwayInUseLandingWeight,
			phasesConfig.RunwayInUseClimbWeight,
			phasesConfig.RunwayInUseDecayRate,
			log,
		),
		logger: log.Named("trajectory"),
		stopCh: make(chan struct{}),
	}
	tt.runwayTracker.SetRunwayData(runwayData)
	tt.wg.Add(1)
	go tt.cleanupLoop()
	tt.logger.Info("轨迹 tracker 已启动",
		logger.Int("buffer_duration_sec", cfg.BufferDurationSec),
		logger.Int("buffer_capacity", cfg.BufferCapacity),
		logger.Int("min_points", cfg.MinPointsForAnalysis),
	)
	return tt
}

// Stop 关闭后台清理 goroutine 并等待其结束。
func (tt *TrajectoryTracker) Stop() {
	close(tt.stopCh)
	tt.wg.Wait()
	tt.logger.Info("轨迹 tracker 已停止")
}

// RecordRunwayLanding 为使用中跑道检测记录一次着陆事件。
// 当在跑道入口附近检测到 T/D 时由服务调用。
func (tt *TrajectoryTracker) RecordRunwayLanding(runwayID string, hex string) {
	if tt.runwayTracker != nil {
		tt.runwayTracker.RecordEvent(runwayID, RunwayEventLanding, hex)
	}
}

// GetRunwayScores 返回前 N 个使用中跑道的分数,供外部消费方使用。
func (tt *TrajectoryTracker) GetRunwayScores(n int) []RunwayScore {
	if tt.runwayTracker != nil {
		return tt.runwayTracker.GetTopScores(n)
	}
	return nil
}

// Ingest 为给定飞行器新增一条快照。如果该飞行器还没有缓冲区,
// 会创建一个。每个抓取周期对每架飞行器调用一次。
func (tt *TrajectoryTracker) Ingest(hex string, snap TrajectorySnapshot) {
	tt.mu.Lock()
	at, ok := tt.aircraft[hex]
	if !ok {
		at = NewAircraftTrajectory(hex, tt.config.BufferCapacity)
		tt.aircraft[hex] = at
	}
	at.AddSnapshot(snap)
	tt.mu.Unlock()
}

// GetTrajectory 返回某架飞行器的轨迹,若未被跟踪则返回 nil。
func (tt *TrajectoryTracker) GetTrajectory(hex string) *AircraftTrajectory {
	tt.mu.RLock()
	at := tt.aircraft[hex]
	tt.mu.RUnlock()
	return at
}

// EnsureDerived 在给定飞行器为脏(自上次计算后有新数据)时,
// 重新计算其派生状态。在读取派生字段之前由 DeterminePhase 调用。
func (tt *TrajectoryTracker) EnsureDerived(hex string) *DerivedState {
	tt.mu.RLock()
	at, ok := tt.aircraft[hex]
	tt.mu.RUnlock()
	if !ok || at.Count == 0 {
		return nil
	}
	if at.DirtyDerived {
		tt.computeDerivedState(at)
		tt.computePredictions(at)
		at.DirtyDerived = false
	}
	return &at.Derived
}

// computePredictions 更新某架飞行器的后报与预报。
// 在 computeDerivedState 之后调用,保持预测与派生状态同步。
func (tt *TrajectoryTracker) computePredictions(at *AircraftTrajectory) {
	computeHindcast(at, &at.Derived)
	computeForecast(at, &at.Derived)
}

// GetHindcast 返回某架飞行器的后报点,若无则返回 nil。
func (tt *TrajectoryTracker) GetHindcast(hex string) []PredictionPoint {
	tt.mu.RLock()
	at, ok := tt.aircraft[hex]
	tt.mu.RUnlock()
	if !ok {
		return nil
	}
	return at.Prediction.Hindcast
}

// GetForecast 返回某架飞行器的预报点,若无则返回 nil。
func (tt *TrajectoryTracker) GetForecast(hex string) []PredictionPoint {
	tt.mu.RLock()
	at, ok := tt.aircraft[hex]
	tt.mu.RUnlock()
	if !ok {
		return nil
	}
	return at.Prediction.Forecast
}

// LivePrediction 是针对某架飞行器、由服务端插值得到的预测状态。
// 用于在两次真实 ADS-B 轮询之间进行高频 WebSocket 更新。
type LivePrediction struct {
	Hex          string    `json:"hex"`
	Lat          float64   `json:"lat"`
	Lon          float64   `json:"lon"`
	Altitude     float64   `json:"altitude"`
	Heading      float64   `json:"heading"`
	Speed        float64   `json:"speed"`
	Confidence   float64   `json:"confidence"`
	Timestamp    time.Time `json:"timestamp"`
	BaseObserved time.Time `json:"base_observed"`
}

// GetLivePredictionsAt 返回所有被跟踪飞行器在时刻 t 的插值实时预测状态。
// 真实的 ADS-B 更新在客户端应始终优先。
func (tt *TrajectoryTracker) GetLivePredictionsAt(t time.Time) []LivePrediction {
	tt.mu.RLock()
	defer tt.mu.RUnlock()

	result := make([]LivePrediction, 0, len(tt.aircraft))
	for hex, at := range tt.aircraft {
		if at == nil || at.Count == 0 {
			continue
		}

		latest := at.Latest()
		if latest == nil || !latest.Valid {
			continue
		}
		if t.Sub(latest.Timestamp) > maxLivePredictionAge {
			continue
		}

		forecast := at.Prediction.Forecast
		if len(forecast) == 0 {
			continue
		}

		p, ok := interpolateLivePredictionFromForecast(latest, forecast, t)
		if !ok {
			continue
		}

		p.Hex = hex
		p.BaseObserved = latest.Timestamp
		result = append(result, p)
	}

	return result
}

func interpolateLivePredictionFromForecast(latest *TrajectorySnapshot, forecast []PredictionPoint, atTime time.Time) (LivePrediction, bool) {
	if latest == nil || len(forecast) == 0 {
		return LivePrediction{}, false
	}

	first := forecast[0]
	latestHeading := first.Heading
	if latest.TrueHeading != 0 {
		latestHeading = latest.TrueHeading
	} else if latest.MagHeading != 0 {
		latestHeading = latest.MagHeading
	} else if latest.Track != 0 {
		latestHeading = latest.Track
	}

	if atTime.Before(first.Timestamp) {
		start := latest.Timestamp
		end := first.Timestamp
		if !end.After(start) {
			return LivePrediction{
				Lat:        first.Lat,
				Lon:        first.Lon,
				Altitude:   first.Altitude,
				Heading:    first.Heading,
				Speed:      first.Speed,
				Confidence: first.Confidence,
				Timestamp:  atTime,
			}, true
		}

		alpha := atTime.Sub(start).Seconds() / end.Sub(start).Seconds()
		if alpha < 0 {
			alpha = 0
		}
		if alpha > 1 {
			alpha = 1
		}

		return LivePrediction{
			Lat:        lerpFloat(latest.Lat, first.Lat, alpha),
			Lon:        lerpFloat(latest.Lon, first.Lon, alpha),
			Altitude:   lerpFloat(latest.AltBaro, first.Altitude, alpha),
			Heading:    lerpAngleDeg(latestHeading, first.Heading, alpha),
			Speed:      lerpFloat(latest.GS, first.Speed, alpha),
			Confidence: lerpFloat(0.95, first.Confidence, alpha),
			Timestamp:  atTime,
		}, true
	}

	for i := 0; i < len(forecast)-1; i++ {
		a := forecast[i]
		b := forecast[i+1]
		if atTime.Before(a.Timestamp) || atTime.After(b.Timestamp) {
			continue
		}

		den := b.Timestamp.Sub(a.Timestamp).Seconds()
		if den <= 0 {
			return LivePrediction{}, false
		}
		alpha := atTime.Sub(a.Timestamp).Seconds() / den
		if alpha < 0 {
			alpha = 0
		}
		if alpha > 1 {
			alpha = 1
		}

		return LivePrediction{
			Lat:        lerpFloat(a.Lat, b.Lat, alpha),
			Lon:        lerpFloat(a.Lon, b.Lon, alpha),
			Altitude:   lerpFloat(a.Altitude, b.Altitude, alpha),
			Heading:    lerpAngleDeg(a.Heading, b.Heading, alpha),
			Speed:      lerpFloat(a.Speed, b.Speed, alpha),
			Confidence: lerpFloat(a.Confidence, b.Confidence, alpha),
			Timestamp:  atTime,
		}, true
	}

	last := forecast[len(forecast)-1]
	if atTime.After(last.Timestamp) {
		return LivePrediction{}, false
	}

	return LivePrediction{
		Lat:        last.Lat,
		Lon:        last.Lon,
		Altitude:   last.Altitude,
		Heading:    last.Heading,
		Speed:      last.Speed,
		Confidence: last.Confidence,
		Timestamp:  atTime,
	}, true
}

func lerpFloat(a, b, t float64) float64 {
	return a + (b-a)*t
}

func lerpAngleDeg(a, b, t float64) float64 {
	d := math.Mod((b-a)+540.0, 360.0) - 180.0
	out := a + d*t
	return math.Mod(out+360.0, 360.0)
}

// ─── 清理 ──────────────────────────────────────────────────────────────────

func (tt *TrajectoryTracker) cleanupLoop() {
	defer tt.wg.Done()
	ticker := time.NewTicker(time.Duration(tt.config.CleanupIntervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tt.cleanup()
		case <-tt.stopCh:
			return
		}
	}
}

func (tt *TrajectoryTracker) cleanup() {
	now := time.Now().UTC()
	staleThreshold := time.Duration(tt.config.StaleTimeoutSec) * time.Second
	tt.mu.Lock()
	removed := 0
	for hex, at := range tt.aircraft {
		if now.Sub(at.LastSeen) > staleThreshold {
			delete(tt.aircraft, hex)
			removed++
		}
	}
	tt.mu.Unlock()
	if removed > 0 {
		tt.logger.Debug("轨迹清理",
			logger.Int("removed", removed),
			logger.Int("remaining", len(tt.aircraft)),
		)
	}
}

// ─── 派生状态计算 ────────────────────────────────────────────────

// computeDerivedState 基于环形缓冲区内容重新计算所有派生字段。
// 每个抓取周期对每架飞行器运行一次(约 90 个数据点,O(N) 单遍)。
func (tt *TrajectoryTracker) computeDerivedState(at *AircraftTrajectory) {
	d := &at.Derived
	*d = DerivedState{ComputedAt: time.Now().UTC()} // 重置

	latest := at.Latest()
	if latest == nil || !latest.Valid {
		return
	}

	// 收集有效快照并检测数据缺口
	shortWindowSec := 10.0
	mediumWindowSec := 30.0
	fetchInterval := float64(tt.config.FetchIntervalSec)
	if fetchInterval < 1 {
		fetchInterval = 1
	}
	gapThreshold := fetchInterval * 2.5

	var (
		allValid    []TrajectorySnapshot
		shortValid  []TrajectorySnapshot
		mediumValid []TrajectorySnapshot
	)

	shortCutoff := latest.Timestamp.Add(-time.Duration(shortWindowSec * float64(time.Second)))
	mediumCutoff := latest.Timestamp.Add(-time.Duration(mediumWindowSec * float64(time.Second)))

	var prevTimestamp time.Time
	gapDetected := false

	at.ForEachSnapshot(func(snap *TrajectorySnapshot) {
		if !snap.Valid {
			return
		}
		allValid = append(allValid, *snap)
		if !snap.Timestamp.Before(mediumCutoff) {
			mediumValid = append(mediumValid, *snap)
		}
		if !snap.Timestamp.Before(shortCutoff) {
			shortValid = append(shortValid, *snap)
		}
		// 缺口检测
		if !prevTimestamp.IsZero() {
			gap := snap.Timestamp.Sub(prevTimestamp).Seconds()
			if gap > gapThreshold {
				gapDetected = true
			}
		}
		prevTimestamp = snap.Timestamp
	})

	d.ValidPointCount = len(allValid)
	d.DataGapDetected = gapDetected
	if len(allValid) >= 2 {
		d.WindowDurationSec = allValid[len(allValid)-1].Timestamp.Sub(allValid[0].Timestamp).Seconds()
	}

	if len(allValid) == 0 {
		return
	}

	// ── 当前到台站的距离与方位 ──
	d.DistToStationNM = MetersToNM(Haversine(latest.Lat, latest.Lon, tt.stationLat, tt.stationLon))
	d.BearingToStation = CalculateBearing(latest.Lat, latest.Lon, tt.stationLat, tt.stationLon)

	// ── 短窗口:平滑后的当前值 ──
	if len(shortValid) > 0 {
		d.GroundSpeedKts = mean(shortValid, func(s TrajectorySnapshot) float64 { return s.GS })
		d.VerticalRateFPM = medianFloat(shortValid, func(s TrajectorySnapshot) float64 { return s.BaroRate })
		d.TrackDeg = circularMean(shortValid, func(s TrajectorySnapshot) float64 { return s.Track })
	} else {
		d.GroundSpeedKts = latest.GS
		d.VerticalRateFPM = latest.BaroRate
		d.TrackDeg = latest.Track
	}

	// ── 短窗口:加速度 ──
	if len(shortValid) >= 2 {
		first := shortValid[0]
		last := shortValid[len(shortValid)-1]
		dt := last.Timestamp.Sub(first.Timestamp).Seconds()
		if dt > 0 {
			d.GSAccelKtsPerSec = (last.GS - first.GS) / dt
			d.VRAccelFPMPerSec = (last.BaroRate - first.BaroRate) / dt
			d.TrackRateDegPerSec = circularDiff(last.Track, first.Track) / dt
		}
	}

	// ── 中窗口:高度统计 ──
	window := mediumValid
	if len(window) == 0 {
		window = allValid
	}

	d.AltMean = mean(window, func(s TrajectorySnapshot) float64 { return s.AltBaro })
	d.AltMin, d.AltMax = minMax(window, func(s TrajectorySnapshot) float64 { return s.AltBaro })
	d.AltTrendFPM = olsSlope(window, func(s TrajectorySnapshot) float64 { return s.AltBaro }) * 60.0 // 把 ft/sec 转换为 fpm

	// ── 中窗口:速度统计 ──
	d.GSMean = mean(window, func(s TrajectorySnapshot) float64 { return s.GS })
	d.GSMin, d.GSMax = minMax(window, func(s TrajectorySnapshot) float64 { return s.GS })
	d.GSTrendKtsPerSec = olsSlope(window, func(s TrajectorySnapshot) float64 { return s.GS })

	// ── 中窗口:垂直速率统计 ──
	d.VRMean = mean(window, func(s TrajectorySnapshot) float64 { return s.BaroRate })
	d.VRStdDev = stdDev(window, func(s TrajectorySnapshot) float64 { return s.BaroRate })

	// ── 中窗口:到台站距离趋势 ──
	d.DistTrendNMPerSec = olsSlopeWithXY(window, func(s TrajectorySnapshot) (float64, float64) {
		return s.Timestamp.Sub(window[0].Timestamp).Seconds(),
			MetersToNM(Haversine(s.Lat, s.Lon, tt.stationLat, tt.stationLon))
	})

	// ── 布尔标志 ──
	cfg := tt.config
	d.IsDescending = d.AltTrendFPM < cfg.DescentVRThresholdFPM && d.VRMean < 0
	d.IsClimbing = d.AltTrendFPM > cfg.ClimbVRThresholdFPM && d.VRMean > 0
	d.IsLevel = (d.AltMax - d.AltMin) < cfg.LevelAltBandFt
	d.IsDecelerating = d.GSTrendKtsPerSec < cfg.DecelerationThreshold
	d.IsAccelerating = d.GSTrendKtsPerSec > cfg.AccelerationThreshold
	d.IsTurning = math.Abs(d.TrackRateDegPerSec) > cfg.TurningRateThresholdDeg
	d.IsApproachingStation = d.DistTrendNMPerSec < -0.001 // 略带负值的阈值,避免噪声
}

// ─── 统计辅助函数 ──────────────────────────────────────────────────────

// mean 计算从快照中提取的某个字段的算术平均值。
func mean(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) float64 {
	if len(snaps) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range snaps {
		sum += extract(s)
	}
	return sum / float64(len(snaps))
}

// minMax 返回某个字段的最小值与最大值。
func minMax(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) (float64, float64) {
	if len(snaps) == 0 {
		return 0, 0
	}
	mn := math.Inf(1)
	mx := math.Inf(-1)
	for _, s := range snaps {
		v := extract(s)
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

// medianFloat 计算某个字段的中位数。会为排序分配临时切片。
func medianFloat(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) float64 {
	n := len(snaps)
	if n == 0 {
		return 0
	}
	vals := make([]float64, n)
	for i, s := range snaps {
		vals[i] = extract(s)
	}
	sort.Float64s(vals)
	if n%2 == 0 {
		return (vals[n/2-1] + vals[n/2]) / 2
	}
	return vals[n/2]
}

// stdDev computes the sample standard deviation of a field.
func stdDev(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) float64 {
	n := len(snaps)
	if n < 2 {
		return 0
	}
	m := mean(snaps, extract)
	sumSq := 0.0
	for _, s := range snaps {
		diff := extract(s) - m
		sumSq += diff * diff
	}
	return math.Sqrt(sumSq / float64(n-1))
}

// olsSlope computes the OLS linear regression slope of a field against time.
// Time is measured in seconds from the first snapshot. Returns the slope in
// units-per-second (e.g., feet-per-second for altitude).
func olsSlope(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) float64 {
	n := len(snaps)
	if n < 2 {
		return 0
	}
	t0 := snaps[0].Timestamp
	var sumT, sumY, sumTY, sumTT float64
	for _, s := range snaps {
		t := s.Timestamp.Sub(t0).Seconds()
		y := extract(s)
		sumT += t
		sumY += y
		sumTY += t * y
		sumTT += t * t
	}
	nf := float64(n)
	denom := nf*sumTT - sumT*sumT
	if math.Abs(denom) < 1e-12 {
		return 0 // All points at same time
	}
	return (nf*sumTY - sumT*sumY) / denom
}

// olsSlopeWithXY computes OLS slope with an explicit (x, y) extractor,
// used for distance-to-station trend where x = time offset, y = distance.
func olsSlopeWithXY(snaps []TrajectorySnapshot, extractXY func(TrajectorySnapshot) (float64, float64)) float64 {
	n := len(snaps)
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumXX float64
	for _, s := range snaps {
		x, y := extractXY(s)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	nf := float64(n)
	denom := nf*sumXX - sumX*sumX
	if math.Abs(denom) < 1e-12 {
		return 0
	}
	return (nf*sumXY - sumX*sumY) / denom
}

// olsR2 computes the R² (coefficient of determination) for the OLS linear
// regression of a field against time. Returns 0 if fewer than 3 points or no
// variance in the data. R² = 1 - SS_res/SS_tot.
func olsR2(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) float64 {
	n := len(snaps)
	if n < 3 {
		return 0
	}
	t0 := snaps[0].Timestamp

	// Compute OLS coefficients (intercept + slope)
	var sumT, sumY, sumTY, sumTT float64
	for _, s := range snaps {
		t := s.Timestamp.Sub(t0).Seconds()
		y := extract(s)
		sumT += t
		sumY += y
		sumTY += t * y
		sumTT += t * t
	}
	nf := float64(n)
	denom := nf*sumTT - sumT*sumT
	if math.Abs(denom) < 1e-12 {
		return 0
	}
	slope := (nf*sumTY - sumT*sumY) / denom
	intercept := (sumY - slope*sumT) / nf

	// Compute R² = 1 - SS_res / SS_tot
	meanY := sumY / nf
	var ssRes, ssTot float64
	for _, s := range snaps {
		t := s.Timestamp.Sub(t0).Seconds()
		y := extract(s)
		predicted := intercept + slope*t
		ssRes += (y - predicted) * (y - predicted)
		ssTot += (y - meanY) * (y - meanY)
	}
	if ssTot < 1e-20 {
		return 0 // No variance
	}
	r2 := 1.0 - ssRes/ssTot
	if r2 < 0 {
		r2 = 0
	}
	return r2
}

// circularMean computes the mean of angles (degrees) handling 0°/360° wraparound.
func circularMean(snaps []TrajectorySnapshot, extract func(TrajectorySnapshot) float64) float64 {
	if len(snaps) == 0 {
		return 0
	}
	var sinSum, cosSum float64
	for _, s := range snaps {
		rad := extract(s) * math.Pi / 180.0
		sinSum += math.Sin(rad)
		cosSum += math.Cos(rad)
	}
	meanRad := math.Atan2(sinSum/float64(len(snaps)), cosSum/float64(len(snaps)))
	deg := meanRad * 180.0 / math.Pi
	return math.Mod(deg+360, 360)
}

// circularDiff returns the signed angular difference (a - b) in degrees,
// handling the 0°/360° wraparound. Result is in [-180, 180].
func circularDiff(a, b float64) float64 {
	diff := a - b
	for diff > 180 {
		diff -= 360
	}
	for diff < -180 {
		diff += 360
	}
	return diff
}
