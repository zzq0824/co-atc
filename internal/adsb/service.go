package adsb

/*
飞行阶段检测系统
==============================

Co-ATC 系统使用两层方法进行飞行阶段检测:

  优先级 1 — 立即地面状态切换(T/O 与 T/D)
    通过 IsFlying() 检测 OnGround 状态翻转时立即识别。
    这些是二元状态变化,无需借助轨迹分析。

  优先级 2 — 其他所有阶段通过轨迹分析
    TrajectoryTracker(trajectory.go, trajectory_phase.go)为每架飞行器维护一个
    约 90 秒滚动窗口的 ADS-B 观测数据,并使用统计分析(OLS 回归、中值滤波、距离趋势)
    将每架飞行器分类到 10 个阶段之一:

      NEW — 新检测到或停于地面
      TAX — 滑行中(1–50 节地速)
      T/O — 起飞(地面→空中切换,为 UI 可见性保留)
      CLB — 初始爬升(跑道航向、靠近机场、hasRecentTakeoff)
      DEP — 离场(爬升远离、低于巡航 — 通用)
      CRZ — 巡航(高于巡航高度)
      ARR — 进场(接近站点、低于巡航、下降/平飞)
      APP — 进近(在最后进近、下降、对齐跑道)
      T/D — 着陆(空中→地面切换,为 UI 可见性保留)
      UNK — 未知(在空但不匹配任何具体条件)

  ARR 不是兜底类别 — 它要求飞行器确实正在接近站点。
  UNK 是诚实的兜底类别,用于任何不匹配的情况。

特殊功能:
  - 基于轨迹的抗噪声:决策基于窗口趋势,而非单次读数
  - 失联着陆:当飞行器在机场附近失联时自动标记为已着陆,
    通过轨迹下降趋势分析增强
  - 阶段保留:T/O 与 T/D 在可配置时长内保持可见
  - 传感器数据校验:在轨迹摄入前修正错误读数
  - 数据间隔检测:处理覆盖间隔而不产生虚假阶段切换
*/

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

const (
	livePredictionBroadcastInterval = 1 * time.Second
	minPredictionConfidence         = 0.65
)

func roundTo(value float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(value*pow) / pow
}

func roundedInterface(value interface{}, decimals int) interface{} {
	switch v := value.(type) {
	case float64:
		return roundTo(v, decimals)
	case *float64:
		if v == nil {
			return nil
		}
		return roundTo(*v, decimals)
	default:
		return value
	}
}

func normalizeRealtimeDelta(delta map[string]interface{}) map[string]interface{} {
	normalized := make(map[string]interface{}, len(delta))
	for key, value := range delta {
		switch key {
		case "lat", "lon":
			normalized[key] = roundedInterface(value, 6)
		case "alt_baro", "alt_geom", "gs", "tas", "ias", "track", "true_heading", "mag_heading", "baro_rate", "geom_rate", "vertical_speed", "vertical_rate":
			normalized[key] = roundedInterface(value, 0)
		default:
			normalized[key] = value
		}
	}
	return normalized
}

func roundPtr(value *float64, decimals int) *float64 {
	if value == nil {
		return nil
	}
	rounded := roundTo(*value, decimals)
	return &rounded
}

func normalizeRealtimeAircraft(aircraft *Aircraft) *Aircraft {
	if aircraft == nil {
		return nil
	}

	clone := *aircraft
	if aircraft.ADSB == nil {
		return &clone
	}

	adsbCopy := *aircraft.ADSB
	if adsbCopy.AltBaro.IsSet() {
		adsbCopy.AltBaro = FlexibleFloat64(roundTo(adsbCopy.AltBaro.Float64(), 0))
	}
	if adsbCopy.AltGeom.IsSet() {
		adsbCopy.AltGeom = FlexibleFloat64(roundTo(adsbCopy.AltGeom.Float64(), 0))
	}
	adsbCopy.Lat = roundPtr(adsbCopy.Lat, 6)
	adsbCopy.Lon = roundPtr(adsbCopy.Lon, 6)
	adsbCopy.GS = roundPtr(adsbCopy.GS, 0)
	adsbCopy.IAS = roundPtr(adsbCopy.IAS, 0)
	adsbCopy.TAS = roundPtr(adsbCopy.TAS, 0)
	adsbCopy.Track = roundPtr(adsbCopy.Track, 0)
	adsbCopy.MagHeading = roundPtr(adsbCopy.MagHeading, 0)
	adsbCopy.TrueHeading = roundPtr(adsbCopy.TrueHeading, 0)
	adsbCopy.BaroRate = roundPtr(adsbCopy.BaroRate, 0)
	adsbCopy.GeomRate = roundPtr(adsbCopy.GeomRate, 0)
	adsbCopy.NavHeading = roundPtr(adsbCopy.NavHeading, 0)
	adsbCopy.NavAltitudeMCP = roundPtr(adsbCopy.NavAltitudeMCP, 0)
	adsbCopy.NavAltitudeFMS = roundPtr(adsbCopy.NavAltitudeFMS, 0)

	clone.ADSB = &adsbCopy
	return &clone
}

// WebSocketServer 定义 WebSocket 服务的接口
type WebSocketServer interface {
	Broadcast(message *websocket.Message)
}

// ReferenceService 定义参考数据查询服务的接口(飞行器、航司)
type ReferenceService interface {
	LookupAircraft(hex string) *ReferenceAircraftInfo
	LookupAirline(code string) string
	LookupAirlineCountry(code string) string
}

// ReferenceAircraftInfo 表示来自参考数据服务的飞行器信息
type ReferenceAircraftInfo struct {
	Hex               string
	Registration      string
	TypeCode          string
	ManufacturerModel string
	Year              string
	Owner             string
}

// Storage 定义飞行器数据存储的接口
type Storage interface {
	GetAll() []*Aircraft
	GetAllWithLastSeenFilter(lastSeenMinutes int) []*Aircraft
	GetAllMinimal(lastSeenMinutes int) []*Aircraft // 最小化模式:跳过阶段历史与日期查询
	GetByHex(hex string) (*Aircraft, bool)
	GetFiltered(
		minAltitude, maxAltitude float64,
		status []string,
		tookOffAfter, tookOffBefore, landedAfter, landedBefore *time.Time,
	) []*Aircraft
	Upsert(aircraft *Aircraft)
	Count() int
	GetAllPositionHistory(hex string) ([]Position, error)
	GetPositionHistoryWithLimit(hex string, limit int) ([]Position, error)

	// 阶段变化方法
	InsertPhaseChange(hex, flight, phase string, timestamp time.Time, adsbId *int) error
	GetPhaseHistory(hex string) ([]PhaseChange, error)
	GetCurrentPhase(hex string) (*PhaseChange, error)
	GetLatestTakeoffTime(hex string) (*time.Time, error)
	GetLatestLandingTime(hex string) (*time.Time, error)
	GetLatestADSBTargetID(hex string) (*int, error)

	// 用于性能优化的批量阶段变化方法
	GetCurrentPhasesBatch(hexCodes []string) (map[string]*PhaseChange, error)
	GetLatestADSBTargetIDsBatch(hexCodes []string) (map[string]*int, error)
	InsertPhaseChangesBatch(changes []PhaseChangeInsert) error

	// 用于获取热路径优化的批量方法
	GetAircraftOnGroundBatch(hexCodes []string) (map[string]bool, error)
	GetLatestADSBDataBatch(hexCodes []string) (map[string]*ADSBTarget, error)
	GetLatestTakeoffTimesBatch(hexCodes []string) (map[string]*time.Time, error)
	GetLatestLandingTimesBatch(hexCodes []string) (map[string]*time.Time, error)

	// 针对非活跃飞行器状态更新的定向查询
	GetStaleActiveAircraft(activeHexCodes []string, cutoff time.Time) ([]*Aircraft, error)
}

// SimulationService 定义模拟服务的接口
type SimulationService interface {
	UpdatePositions()
	GenerateADSBData() []ADSBTarget
	IsSimulated(hex string) bool
	GetAllAircraft() interface{}                                           // 返回模拟飞行器数据
	GetAircraft(hex string) (interface{}, bool)                            // 返回特定的模拟飞行器
	UpdateControls(hex string, heading, speed, verticalRate float64) error // 更新模拟控制
}

// Service 是 ADS-B 数据处理的主服务
type Service struct {
	client             *Client
	storage            Storage
	fetchInterval      time.Duration
	logger             *logger.Logger
	lastFetchTime      time.Time
	lastFetchStatus    bool
	mu                 sync.RWMutex
	stopCh             chan struct{}
	wg                 sync.WaitGroup
	refService         ReferenceService          // 参考数据查询服务
	stationLat         float64                   // 配置中的站点纬度
	stationLon         float64                   // 配置中的站点经度
	stationElevFeet    float64                   // 站点海拔(英尺)
	overrideLat        *float64                  // 覆盖站点纬度(nil = 使用配置)
	overrideLon        *float64                  // 覆盖站点经度(nil = 使用配置)
	overrideMutex      sync.RWMutex              // 保护覆盖坐标
	wsServer           WebSocketServer           // 用于广播事件的 WebSocket 服务
	signalLostTimeout  time.Duration             // 飞行器被标记为 signal_lost 的超时时间
	runwayData         RunwayData                // 用于进近检测的跑道数据
	flightPhasesConfig config.FlightPhasesConfig // 飞行阶段配置
	changeDetector     *ChangeDetector           // 跟踪飞行器变化
	broadcastChan      chan []AircraftChange     // 广播变化的通道
	simulationService  SimulationService         // 用于模拟飞行器的模拟服务
	trajectoryTracker  *TrajectoryTracker        // 基于轨迹的阶段检测
}

// AircraftBulkResponse 表示包含批量飞行器数据的服务响应
type AircraftBulkResponse struct {
	Aircraft []*Aircraft    `json:"aircraft"`
	Count    int            `json:"count"`
	Counts   AircraftCounts `json:"counts"`
}

// NewService 创建新的 ADS-B 服务
func NewService(
	client *Client,
	storage Storage,
	fetchInterval time.Duration,
	logger *logger.Logger,
	stationCfg config.StationConfig,
	adsbCfg config.ADSBConfig,
	flightPhasesConfig config.FlightPhasesConfig,
	wsServer WebSocketServer,
	simulationService SimulationService,
) *Service {
	// 如未配置则设置默认失联超时
	signalLostTimeout := time.Duration(adsbCfg.SignalLostTimeoutSecs) * time.Second
	if signalLostTimeout == 0 {
		signalLostTimeout = 60 * time.Second // 默认 60 秒
	}

	service := &Service{
		client:             client,
		storage:            storage,
		fetchInterval:      fetchInterval,
		logger:             logger.Named("adsb"),
		stopCh:             make(chan struct{}),
		stationLat:         stationCfg.Latitude,
		stationLon:         stationCfg.Longitude,
		stationElevFeet:    float64(stationCfg.ElevationFeet),
		wsServer:           wsServer,
		signalLostTimeout:  signalLostTimeout,
		flightPhasesConfig: flightPhasesConfig,
		simulationService:  simulationService,
	}

	// 始终为飞行器更新启用 WebSocket 流
	logger.Info("初始化用于飞行器流的 WebSocket 变更检测")
	service.changeDetector = NewChangeDetector(logger)

	// 为预测函数设置配置
	predictionConfig := &Config{
		Station: struct {
			Latitude  float64
			Longitude float64
		}{
			Latitude:  stationCfg.Latitude,
			Longitude: stationCfg.Longitude,
		},
	}
	SetConfig(predictionConfig)

	// 初始化用于阶段检测的轨迹跟踪器
	if flightPhasesConfig.Enabled {
		fetchIntervalSec := adsbCfg.FetchIntervalSecs
		if fetchIntervalSec < 1 {
			fetchIntervalSec = 1
		}
		bufferCap := flightPhasesConfig.TrajectoryBufferDurationSec/fetchIntervalSec + 10 // +10 余量
		service.trajectoryTracker = NewTrajectoryTracker(
			TrajectoryConfig{
				BufferDurationSec:       flightPhasesConfig.TrajectoryBufferDurationSec,
				BufferCapacity:          bufferCap,
				FetchIntervalSec:        fetchIntervalSec,
				MinPointsForAnalysis:    flightPhasesConfig.TrajectoryMinPoints,
				StaleTimeoutSec:         flightPhasesConfig.TrajectoryStaleTimeoutSec,
				CleanupIntervalSec:      flightPhasesConfig.TrajectoryCleanupIntervalSec,
				DescentVRThresholdFPM:   flightPhasesConfig.TrajectoryDescentThresholdFPM,
				ClimbVRThresholdFPM:     flightPhasesConfig.TrajectoryClimbThresholdFPM,
				LevelAltBandFt:          flightPhasesConfig.TrajectoryLevelBandFt,
				TurningRateThresholdDeg: flightPhasesConfig.TrajectoryTurningRateDeg,
				DecelerationThreshold:   -0.5,
				AccelerationThreshold:   0.5,
			},
			stationCfg.Latitude,
			stationCfg.Longitude,
			service.runwayData,
			&service.flightPhasesConfig,
			logger,
		)
	}

	return service
}

// startBroadcastWorker 启动通过 WebSocket 广播飞行器变化的工作协程
func (s *Service) startBroadcastWorker() {
	go func() {
		for changes := range s.broadcastChan {
			for _, change := range changes {
				s.broadcastAircraftChange(change)
			}
		}
	}()
}

// broadcastAircraftChange 通过 WebSocket 广播单个飞行器变化
func (s *Service) broadcastAircraftChange(change AircraftChange) {
	var messageType string
	switch change.Type {
	case "added":
		messageType = websocket.MessageTypeAircraftAdded
	case "updated":
		messageType = websocket.MessageTypeAircraftUpdate
	case "removed":
		messageType = websocket.MessageTypeAircraftRemoved
	}

	data := map[string]interface{}{
		"type": change.Type,
		"hex":  change.Hex,
	}

	observedAt := time.Now().UTC()
	if change.Aircraft != nil && !change.Aircraft.LastSeen.IsZero() {
		observedAt = change.Aircraft.LastSeen.UTC()
	}
	data["observed_at"] = observedAt.Format(time.RFC3339Nano)

	// 对于 "added",发送完整的飞行器对象
	// 对于 "updated",仅发送增量(变化字段)
	// 对于 "removed",仅发送 hex
	if change.Aircraft != nil {
		data["aircraft"] = normalizeRealtimeAircraft(change.Aircraft)
	}
	if change.Delta != nil {
		data["delta"] = normalizeRealtimeDelta(change.Delta)
	}

	message := &websocket.Message{
		Type: messageType,
		Data: data,
	}

	if s.wsServer != nil {
		s.wsServer.Broadcast(message)
	}
}

// predictionBroadcastLoop 定期发布基于轨迹的预测状态,
// 以便客户端能在真实 ADS-B 轮询间隔中平滑动画。
func (s *Service) predictionBroadcastLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(livePredictionBroadcastInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if s.wsServer == nil || s.trajectoryTracker == nil {
				continue
			}

			now := time.Now().UTC()
			predictions := s.trajectoryTracker.GetLivePredictionsAt(now)
			for _, p := range predictions {
				if p.Confidence < minPredictionConfidence {
					continue
				}

				s.wsServer.Broadcast(&websocket.Message{
					Type: websocket.MessageTypeAircraftPredictedState,
					Data: map[string]interface{}{
						"hex":          p.Hex,
						"predicted":    true,
						"based_on":     p.BaseObserved.Format(time.RFC3339Nano),
						"predicted_at": p.Timestamp.Format(time.RFC3339Nano),
						"delta": map[string]interface{}{
							"lat":          roundTo(p.Lat, 6),
							"lon":          roundTo(p.Lon, 6),
							"alt_baro":     roundTo(p.Altitude, 0),
							"gs":           roundTo(p.Speed, 0),
							"track":        roundTo(p.Heading, 0),
							"true_heading": roundTo(p.Heading, 0),
							"mag_heading":  roundTo(p.Heading, 0),
							"confidence":   roundTo(p.Confidence, 3),
							"source":       "predicted",
						},
					},
				})
			}

		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// sendPhaseChangeAlert 通过 WebSocket 发送阶段变化告警
func (s *Service) sendPhaseChangeAlert(aircraft *Aircraft, fromPhase, toPhase string, runwayInfo *RunwayApproachInfo) {
	s.sendPhaseChangeAlertWithEvent(aircraft, fromPhase, toPhase, "phase_change", runwayInfo)
}

// sendPhaseChangeAlertWithEvent 通过 WebSocket 发送带事件类型的阶段变化告警
func (s *Service) sendPhaseChangeAlertWithEvent(aircraft *Aircraft, fromPhase, toPhase, eventType string, runwayInfo *RunwayApproachInfo) {
	if s.wsServer != nil {
		lat, lon, _ := aircraft.ADSB.Position()
		alert := PhaseChangeAlert{
			Type:      "phase_change",
			Hex:       aircraft.Hex,
			Flight:    aircraft.Flight,
			FromPhase: fromPhase,
			ToPhase:   toPhase,
			EventType: eventType,
			Timestamp: time.Now().UTC(),
			Location: struct {
				Lat float64 `json:"lat"`
				Lon float64 `json:"lon"`
				Alt float64 `json:"alt"`
			}{
				Lat: roundTo(lat, 6),
				Lon: roundTo(lon, 6),
				Alt: aircraft.ADSB.AltBaro.Float64(),
			},
			RunwayInfo: runwayInfo,
		}

		s.wsServer.Broadcast(&websocket.Message{
			Type: "phase_change",
			Data: map[string]interface{}{
				"alert": alert,
			},
		})

		s.logger.Info("已发送阶段变化告警",
			logger.String("hex", aircraft.Hex),
			logger.String("flight", aircraft.Flight),
			logger.String("transition", fromPhase+" → "+toPhase),
			logger.String("event_type", eventType),
			logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
			logger.Bool("on_ground", aircraft.OnGround),
		)
	}
}

// Start 启动 ADS-B 服务
func (s *Service) Start(ctx context.Context) error {
	s.logger.Info("启动 ADS-B 服务",
		logger.Duration("fetch_interval", s.fetchInterval),
	)

	// 初次获取
	if err := s.fetchAndProcess(ctx); err != nil {
		s.logger.Error("获取初始 ADS-B 数据失败", logger.Error(err))
		s.setFetchStatus(false)
		return fmt.Errorf("初次 ADS-B 获取失败: %w", err)
	}
	s.setFetchStatus(true)

	// 启动后台获取
	s.wg.Add(1)
	go s.fetchLoop(ctx)

	// 启动后台预测流(在 ADS-B 轮询间隔之间)
	if s.trajectoryTracker != nil && s.wsServer != nil {
		s.wg.Add(1)
		go s.predictionBroadcastLoop(ctx)
	}

	return nil
}

// Stop 停止 ADS-B 服务
func (s *Service) Stop() {
	s.logger.Info("正在停止 ADS-B 服务")
	close(s.stopCh)
	s.wg.Wait()
	if s.trajectoryTracker != nil {
		s.trajectoryTracker.Stop()
	}
	s.logger.Info("ADS-B 服务已停止")
}

// fetchLoop 定期获取并处理 ADS-B 数据
func (s *Service) fetchLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.fetchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.fetchAndProcess(ctx); err != nil {
				s.logger.Error("获取 ADS-B 数据失败", logger.Error(err))
				s.setFetchStatus(false)
			} else {
				s.setFetchStatus(true)
			}
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// fetchAndProcess 获取并处理 ADS-B 数据
func (s *Service) fetchAndProcess(ctx context.Context) error {
	// 获取原始数据
	rawData, err := s.client.FetchData(ctx)
	if err != nil {
		return err
	}

	// 更新模拟飞行器位置并注入模拟数据
	if s.simulationService != nil {
		s.simulationService.UpdatePositions()
		simulatedTargets := s.simulationService.GenerateADSBData()

		// 将模拟飞行器追加到原始数据
		rawData.Aircraft = append(rawData.Aircraft, simulatedTargets...)

		s.logger.Debug("已将模拟飞行器注入 ADSB 数据",
			logger.Int("count", len(simulatedTargets)))
	}

	// 处理原始数据(现在包含模拟飞行器)
	newAircraft := s.ProcessRawData(rawData)

	// 创建活跃飞行器 hex 代码的映射
	activeAircraft := make(map[string]bool)
	for _, a := range newAircraft {
		activeAircraft[a.Hex] = true
	}

	// 收集所有 hex 代码以用于批量操作
	hexCodes := make([]string, len(newAircraft))
	for i, a := range newAircraft {
		hexCodes[i] = a.Hex
	}

	// 性能优化:批量查询将每周期 ~8000 次单飞行器查询替换为 6 次批量查询
	existingOnGround, _ := s.storage.GetAircraftOnGroundBatch(hexCodes)
	existingADSB, _ := s.storage.GetLatestADSBDataBatch(hexCodes)
	takeoffTimes, _ := s.storage.GetLatestTakeoffTimesBatch(hexCodes)
	landingTimes, _ := s.storage.GetLatestLandingTimesBatch(hexCodes)
	currentPhases, _ := s.storage.GetCurrentPhasesBatch(hexCodes)
	adsbTargetIDs, _ := s.storage.GetLatestADSBTargetIDsBatch(hexCodes)

	// 处理每架飞行器以确定地面状态及起飞/着陆检测
	for _, a := range newAircraft {
		// 使用预获取的批量数据检查飞行器是否在数据库中存在
		_, found := existingOnGround[a.Hex]

		var prevTAS, prevGS, prevAlt float64
		if found {
			if adsbData, ok := existingADSB[a.Hex]; ok && adsbData != nil {
				prevTAS = NumberOrZero(adsbData.TAS)
				prevGS = NumberOrZero(adsbData.GS)
				prevAlt = adsbData.AltBaro.Float64()
			}
		}

		// 校验并修正传感器数据中的潜在错误
		currentTAS := NumberOrZero(a.ADSB.TAS)
		currentGS := NumberOrZero(a.ADSB.GS)
		lat, lon, _ := a.ADSB.Position()
		correctedTAS, correctedGS, correctedAlt := ValidateSensorData(
			currentTAS, currentGS, a.ADSB.AltBaro.Float64(),
			prevTAS, prevGS, prevAlt,
			lat, lon, s.stationLat, s.stationLon,
			s.flightPhasesConfig.AirportRangeNM,
			&s.flightPhasesConfig,
		)

		// 将修正值应用回 ADSB 数据,使整个流水线
		// (阶段检测、数据库存储、变更检测器)使用一致的修正数据
		if correctedTAS != currentTAS || correctedGS != currentGS || correctedAlt != a.ADSB.AltBaro.Float64() {
			if found {
				s.logger.Debug("传感器数据已修正",
					logger.String("hex", a.Hex),
					logger.String("flight", a.Flight),
					logger.Float64("original_tas", currentTAS),
					logger.Float64("corrected_tas", correctedTAS),
					logger.Float64("original_gs", currentGS),
					logger.Float64("corrected_gs", correctedGS),
					logger.Float64("original_alt", a.ADSB.AltBaro.Float64()),
					logger.Float64("corrected_alt", correctedAlt),
				)
			}
			if correctedTAS != currentTAS {
				a.ADSB.TAS = NumberPtr(correctedTAS)
			}
			if correctedGS != currentGS {
				a.ADSB.GS = NumberPtr(correctedGS)
			}
			if correctedAlt != a.ADSB.AltBaro.Float64() {
				a.ADSB.AltBaro = FlexibleFloat64(correctedAlt)
			}
		}

		if a.ADSB.SourceType == SourceTypeExternalOpenSky && a.ADSB.OnGroundReported != nil {
			// 在可用时优先使用源提供的 OpenSky on_ground。
			a.OnGround = *a.ADSB.OnGroundReported
		} else {
			// 使用修正值与配置判断飞行器当前是否在飞行
			currentlyFlying := IsFlying(NumberOrZero(a.ADSB.TAS), NumberOrZero(a.ADSB.GS), a.ADSB.AltBaro.Float64(), &s.flightPhasesConfig)

			// 防止因缺失数据而误判 on_ground:Mode S 首次接触时可能上报
			// 零高度、零速度且无位置。这是"无可用数据",而不是"在地面"。
			// 此处默认为 on_ground 会导致下一周期真实数据到达时产生假 T/O。
			noUsableData := a.ADSB.AltBaro.Float64() == 0 && NumberOrZero(a.ADSB.GS) == 0 && NumberOrZero(a.ADSB.TAS) == 0
			if noUsableData {
				// 完全没有传感器数据 — 已知则保留先前的地面状态,
				// 否则假设在空中(比之后触发假 T/O 更安全)
				if prevOnGround, known := existingOnGround[a.Hex]; known {
					a.OnGround = prevOnGround
				} else {
					a.OnGround = false
				}
			} else {
				a.OnGround = !currentlyFlying
			}
		}

		// 将修正后的数据送入轨迹跟踪器用于阶段分析
		if s.trajectoryTracker != nil && a.ADSB != nil {
			snap := TrajectorySnapshotFromADSB(a.ADSB, a.OnGround, time.Now().UTC())
			s.trajectoryTracker.Ingest(a.Hex, snap)
		}

		if found {
			// 使用预获取的起飞与着陆时间
			a.DateTookoff = takeoffTimes[a.Hex]
			a.DateLanded = landingTimes[a.Hex]
		} else {
			// 新飞行器 — 记录日志并发送 WebSocket 通知
			s.logger.Info("检测到新飞行器",
				logger.String("hex", a.Hex),
				logger.String("flight", a.Flight),
				logger.Float64("altitude", a.ADSB.AltBaro.Float64()),
				logger.Bool("on_ground", a.OnGround),
			)
			if s.wsServer != nil {
				s.wsServer.Broadcast(&websocket.Message{
					Type: "status_update",
					Data: map[string]interface{}{
						"hex":        a.Hex,
						"flight":     a.Flight,
						"altitude":   a.ADSB.AltBaro.Float64(),
						"on_ground":  a.OnGround,
						"timestamp":  time.Now().UTC().Format(time.RFC3339),
						"new_status": "new_aircraft",
					},
				})
			}
		}
	}

	// 优先级 1:处理立即地面状态切换(起飞/着陆)
	immediatePhaseChanges := s.detectGroundStateTransitions(newAircraft, existingOnGround, currentPhases, adsbTargetIDs)
	if len(immediatePhaseChanges) > 0 {
		err := s.storage.InsertPhaseChangesBatch(immediatePhaseChanges)
		if err != nil {
			s.logger.Error("插入立即地面切换阶段失败", logger.Error(err))
		} else {
			s.sendImmediateGroundTransitionAlerts(immediatePhaseChanges)
		}
	}

	// 更新数据库中的所有飞行器
	for _, a := range newAircraft {
		s.storage.Upsert(a)
	}

	// 更新已不再活跃的现有飞行器的状态
	s.updateAircraftStatus(activeAircraft)

	// 优先级 2:处理所有其他阶段变化(常规阶段检测)
	newPhaseChanges := s.processPhaseChangesBatch(newAircraft, immediatePhaseChanges, currentPhases, adsbTargetIDs, takeoffTimes)

	s.setLastFetchTime(time.Now().UTC())

	// 使用增强的 newAircraft 检测并广播变化(无需读数据库)
	if s.changeDetector != nil {
		// 用批量数据 + 新变化的阶段数据增强 newAircraft
		phaseMap := make(map[string]PhaseChange)
		for hex, phase := range currentPhases {
			if phase != nil {
				phaseMap[hex] = *phase
			}
		}
		// 用立即阶段变化覆盖
		for _, change := range immediatePhaseChanges {
			phaseMap[change.Hex] = PhaseChange{Phase: change.Phase, Timestamp: change.Timestamp}
		}
		// 用新检测到的阶段变化覆盖
		for _, change := range newPhaseChanges {
			phaseMap[change.Hex] = PhaseChange{Phase: change.Phase, Timestamp: change.Timestamp}
		}
		for _, a := range newAircraft {
			if phase, ok := phaseMap[a.Hex]; ok {
				a.Phase = &PhaseData{Current: []PhaseChange{phase}}
			}
		}

		// 使用 BSDB 和模拟数据进行增强(内存查询)
		s.enrichWithRefData(newAircraft)
		s.updateSimulationFields(newAircraft)
		s.enrichWithATCDerivedData(newAircraft)

		// 叠加基于轨迹的预测(在阶段检测后重新计算)
		if s.trajectoryTracker != nil {
			for _, a := range newAircraft {
				if forecast := s.trajectoryTracker.GetForecast(a.Hex); len(forecast) > 0 {
					a.Future = PredictionPointsToPositions(forecast)
				}
				if hindcast := s.trajectoryTracker.GetHindcast(a.Hex); len(hindcast) > 0 {
					a.Hindcast = PredictionPointsToPositions(hindcast)
				}
			}
		}

		changes := s.changeDetector.DetectChanges(newAircraft)
		if len(changes) > 0 {
			s.logger.Debug("检测到飞行器变化",
				logger.Int("change_count", len(changes)))

			for _, change := range changes {
				s.broadcastAircraftChange(change)
			}
		}
	}

	s.logger.Debug("已更新飞行器数据",
		logger.Int("count", len(newAircraft)),
		logger.Int("total", s.storage.Count()),
	)

	return nil
}

// updateSimulationFields 更新飞行器的 IsSimulated 字段和模拟控制
func (s *Service) updateSimulationFields(aircraft []*Aircraft) {
	for _, a := range aircraft {
		if a.ADSB != nil {
			a.IsSimulated = (s.simulationService != nil && s.simulationService.IsSimulated(a.Hex)) || a.ADSB.Type == "sim"

			// 如果是模拟飞行器则更新模拟控制
			if a.IsSimulated && a.SimulationControls == nil {
				a.SimulationControls = &SimulationControls{
					TargetHeading:      NumberOrZero(a.ADSB.TrueHeading),
					TargetSpeed:        NumberOrZero(a.ADSB.TAS),
					TargetVerticalRate: NumberOrZero(a.ADSB.BaroRate),
				}
			}
		}
	}
}

func (s *Service) enrichWithATCDerivedData(aircraft []*Aircraft) {
	for _, a := range aircraft {
		if a == nil || a.ADSB == nil {
			continue
		}
		a.ADSB.ATCDerived = computeATCDerivedMetrics(a.ADSB, a.Distance)
	}
}

// SetReferenceService 设置参考数据查询服务并更新跑道数据
func (s *Service) SetReferenceService(refService ReferenceService) {
	s.refService = refService
}

// SetRunwayData 设置用于进近/离场检测的跑道数据
// 在参考服务提供本场跑道数据后调用
func (s *Service) SetRunwayData(data RunwayData) {
	s.runwayData = data
	if s.trajectoryTracker != nil {
		s.trajectoryTracker.runwayData = data
	}
}

// GetRunwayData 返回当前跑道数据
func (s *Service) GetRunwayData() RunwayData {
	return s.runwayData
}

// GetRunwayInUseScores 返回前 N 个使用中跑道的概率分数。
func (s *Service) GetRunwayInUseScores(n int) []RunwayScore {
	if s.trajectoryTracker != nil {
		return s.trajectoryTracker.GetRunwayScores(n)
	}
	return nil
}

// enrichWithRefData 用参考数据(aircraft.csv)增强飞行器数据
func (s *Service) enrichWithRefData(aircraft []*Aircraft) {
	if s.refService == nil {
		return
	}

	for _, a := range aircraft {
		if info := s.refService.LookupAircraft(a.Hex); info != nil {
			a.BSDB = &BSDBData{
				Registration:     info.Registration,
				ICAOTypeCode:     info.TypeCode,
				Manufacturer:     info.ManufacturerModel,
				Type:             info.ManufacturerModel,
				RegisteredOwners: info.Owner,
			}
		}
		// 从航司代码增强航司国家(不存储于数据库,在运行时派生)
		if a.Airline != "" && a.AirlineCountry == "" && len(a.Flight) >= 3 {
			a.AirlineCountry = s.refService.LookupAirlineCountry(strings.ToUpper(a.Flight[:3]))
		}
	}
}

// UpdateSimulationControls 更新模拟飞行器的控制参数
func (s *Service) UpdateSimulationControls(hex string, heading, speed, verticalRate float64) error {
	if s.simulationService == nil {
		return fmt.Errorf("模拟服务不可用")
	}
	return s.simulationService.UpdateControls(hex, heading, speed, verticalRate)
}

// GetAllAircraft 返回所有飞行器
func (s *Service) GetAllAircraft() []*Aircraft {
	aircraft := s.storage.GetAll()
	s.updateSimulationFields(aircraft)
	s.enrichWithRefData(aircraft)
	s.enrichWithATCDerivedData(aircraft)
	return aircraft
}

// GetAllAircraftWithLastSeenFilter 返回最近 N 分钟内出现的飞行器
// 使用数据库级过滤,以在大型数据库上获得更佳性能
func (s *Service) GetAllAircraftWithLastSeenFilter(lastSeenMinutes int) []*Aircraft {
	aircraft := s.storage.GetAllWithLastSeenFilter(lastSeenMinutes)
	s.updateSimulationFields(aircraft)
	s.enrichWithRefData(aircraft)
	s.enrichWithATCDerivedData(aircraft)
	return aircraft
}

// GetAllAircraftMinimal 返回带最小数据的飞行器(为简化 API 优化)
// 跳过阶段历史和起飞/着陆时间查询以获得更佳性能
func (s *Service) GetAllAircraftMinimal(lastSeenMinutes int) []*Aircraft {
	aircraft := s.storage.GetAllMinimal(lastSeenMinutes)
	s.updateSimulationFields(aircraft)
	s.enrichWithRefData(aircraft)
	s.enrichWithATCDerivedData(aircraft)
	return aircraft
}

// GetAircraftByHex 通过 hex ID 返回飞行器
func (s *Service) GetAircraftByHex(hex string) (*Aircraft, bool) {
	aircraft, found := s.storage.GetByHex(hex)
	if found && aircraft != nil {
		s.updateSimulationFields([]*Aircraft{aircraft})
		s.enrichWithRefData([]*Aircraft{aircraft})
		s.enrichWithATCDerivedData([]*Aircraft{aircraft})

		// 叠加基于轨迹的预测(后向预测 + 前向预测)
		if s.trajectoryTracker != nil {
			if forecast := s.trajectoryTracker.GetForecast(hex); len(forecast) > 0 {
				aircraft.Future = PredictionPointsToPositions(forecast)
			}
			if hindcast := s.trajectoryTracker.GetHindcast(hex); len(hindcast) > 0 {
				aircraft.Hindcast = PredictionPointsToPositions(hindcast)
			}
		}
	}
	return aircraft, found
}

// GetAllPositionHistory 返回飞行器的所有位置历史
func (s *Service) GetAllPositionHistory(hex string) ([]Position, error) {
	return s.storage.GetAllPositionHistory(hex)
}

// GetPositionHistoryWithLimit 以指定限制返回飞行器的位置历史
func (s *Service) GetPositionHistoryWithLimit(hex string, limit int) ([]Position, error) {
	return s.storage.GetPositionHistoryWithLimit(hex, limit)
}

// GetPhaseHistory 返回飞行器的阶段变化历史(最新优先)
func (s *Service) GetPhaseHistory(hex string) ([]PhaseChange, error) {
	return s.storage.GetPhaseHistory(hex)
}

// GetFilteredAircraft 返回按高度、状态和日期范围过滤的飞行器
func (s *Service) GetFilteredAircraft(
	minAltitude, maxAltitude float64,
	status []string,
	tookOffAfter, tookOffBefore, landedAfter, landedBefore *time.Time,
) []*Aircraft {
	aircraft := s.storage.GetFiltered(
		minAltitude, maxAltitude,
		status,
		tookOffAfter, tookOffBefore, landedAfter, landedBefore,
	)
	s.updateSimulationFields(aircraft)
	s.enrichWithRefData(aircraft)
	s.enrichWithATCDerivedData(aircraft)
	return aircraft
}

// GetFilteredAircraftSimple 是简化版本,用于向后兼容
func (s *Service) GetFilteredAircraftSimple(minAltitude, maxAltitude float64, status ...string) []*Aircraft {
	aircraft := s.storage.GetFiltered(minAltitude, maxAltitude, status, nil, nil, nil, nil)
	s.updateSimulationFields(aircraft)
	s.enrichWithRefData(aircraft)
	s.enrichWithATCDerivedData(aircraft)
	return aircraft
}

// HandleBulkRequest 处理客户端的批量飞行器数据请求
func (s *Service) HandleBulkRequest(filters map[string]interface{}) (*AircraftBulkResponse, error) {
	// 从请求解析过滤条件
	minAltitude := 0.0
	maxAltitude := 60000.0
	var status []string
	lastSeenMinutes := 0
	excludeOtherAirportsGrounded := false
	showAir := true
	showGround := true
	var phases []string

	// 提取过滤条件
	if val, ok := filters["min_altitude"].(float64); ok {
		minAltitude = val
	}
	if val, ok := filters["max_altitude"].(float64); ok {
		maxAltitude = val
	}
	if val, ok := filters["status"].([]interface{}); ok {
		for _, s := range val {
			if str, ok := s.(string); ok {
				status = append(status, str)
			}
		}
	}
	if val, ok := filters["last_seen_minutes"].(float64); ok {
		lastSeenMinutes = int(val)
	}
	if val, ok := filters["exclude_other_airports_grounded"].(bool); ok {
		excludeOtherAirportsGrounded = val
	}
	if val, ok := filters["show_air"].(bool); ok {
		showAir = val
	}
	if val, ok := filters["show_ground"].(bool); ok {
		showGround = val
	}
	if val, ok := filters["phases"].([]interface{}); ok {
		for _, p := range val {
			if str, ok := p.(string); ok {
				phases = append(phases, str)
			}
		}
	}

	// 如果空中和地面都被禁用,则返回空结果
	if !showAir && !showGround {
		return &AircraftBulkResponse{
			Aircraft: []*Aircraft{},
			Count:    0,
			Counts: AircraftCounts{
				GroundActive: 0,
				GroundTotal:  0,
				AirActive:    0,
				AirTotal:     0,
			},
		}, nil
	}

	// 使用现有过滤逻辑获取过滤后的飞行器
	var aircraft []*Aircraft
	if minAltitude > 0 || maxAltitude < 60000 || len(status) > 0 {
		aircraft = s.GetFilteredAircraft(minAltitude, maxAltitude, status, nil, nil, nil, nil)
	} else {
		aircraft = s.GetAllAircraft()
	}

	// 应用其他过滤条件
	if lastSeenMinutes > 0 {
		aircraft = s.filterByLastSeen(aircraft, lastSeenMinutes)
	}

	if excludeOtherAirportsGrounded {
		aircraft = s.filterByAirportGrounded(aircraft)
	}

	// 应用空中/地面和阶段过滤
	aircraft = s.filterByAirGroundAndPhases(aircraft, showAir, showGround, phases)

	// 计算计数
	groundActive, groundTotal, airActive, airTotal := s.calculateCounts(aircraft)

	return &AircraftBulkResponse{
		Aircraft: aircraft,
		Count:    len(aircraft),
		Counts: AircraftCounts{
			GroundActive: groundActive,
			GroundTotal:  groundTotal,
			AirActive:    airActive,
			AirTotal:     airTotal,
		},
	}, nil
}

func (s *Service) filterByLastSeen(aircraft []*Aircraft, minutes int) []*Aircraft {
	cutoffTime := time.Now().UTC().Add(-time.Duration(minutes) * time.Minute)
	filtered := make([]*Aircraft, 0)
	for _, a := range aircraft {
		if a.LastSeen.After(cutoffTime) {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// filterByAirGroundAndPhases 基于空中/地面范围和阶段过滤飞行器
func (s *Service) filterByAirGroundAndPhases(aircraft []*Aircraft, showAir, showGround bool, phases []string) []*Aircraft {
	filtered := make([]*Aircraft, 0)

	for _, a := range aircraft {
		// 应用空中/地面过滤
		if a.OnGround && !showGround {
			continue
		}
		if !a.OnGround && !showAir {
			continue
		}

		// 如果指定了阶段则应用阶段过滤
		if len(phases) > 0 {
			phaseMatch := false
			if a.Phase != nil && len(a.Phase.Current) > 0 {
				currentPhase := a.Phase.Current[0].Phase
				for _, phase := range phases {
					if currentPhase == phase {
						phaseMatch = true
						break
					}
				}
			}
			if !phaseMatch {
				continue
			}
		}

		filtered = append(filtered, a)
	}

	return filtered
}

func (s *Service) filterByAirportGrounded(aircraft []*Aircraft) []*Aircraft {
	filtered := make([]*Aircraft, 0)
	airportRangeNM := 5.0 // 默认范围,应来自配置

	for _, a := range aircraft {
		if !a.OnGround {
			filtered = append(filtered, a)
		} else if a.ADSB != nil && a.ADSB.HasPosition() {
			lat, lon, _ := a.ADSB.Position()
			// 计算到站点的距离并应用过滤
			distMeters := Haversine(lat, lon, s.stationLat, s.stationLon)
			distNM := MetersToNM(distMeters)
			if distNM <= airportRangeNM {
				filtered = append(filtered, a)
			}
		}
	}
	return filtered
}

func (s *Service) calculateCounts(aircraft []*Aircraft) (int, int, int, int) {
	groundActive, groundTotal, airActive, airTotal := 0, 0, 0, 0

	for _, a := range aircraft {
		if a.OnGround {
			groundTotal++
			if a.Status == "active" {
				groundActive++
			}
		} else {
			airTotal++
			if a.Status == "active" {
				airActive++
			}
		}
	}

	return groundActive, groundTotal, airActive, airTotal
}

// GetStatus 返回服务状态
func (s *Service) GetStatus() (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastFetchTime, s.lastFetchStatus
}

// GetSourceStatus 返回 ADS-B 源健康状况和元数据快照。
func (s *Service) GetSourceStatus() SourceStatus {
	if s.client == nil {
		return SourceStatus{
			Status: "error",
			Aircraft: SourceChannelStatus{
				Available: false,
				LastError: "ADS-B 客户端未初始化",
				Data:      nil,
			},
			Receiver: SourceChannelStatus{Available: false, Data: nil},
			Stats:    SourceChannelStatus{Available: false, Data: nil},
		}
	}

	return s.client.GetSourceStatus()
}

// setLastFetchTime 设置最近一次获取时间
func (s *Service) setLastFetchTime(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFetchTime = t
}

// setFetchStatus 设置获取状态
func (s *Service) setFetchStatus(status bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFetchStatus = status
}

// SetStationOverride 设置站点位置的覆盖坐标
func (s *Service) SetStationOverride(lat, lon float64) {
	s.overrideMutex.Lock()
	defer s.overrideMutex.Unlock()

	s.overrideLat = &lat
	s.overrideLon = &lon

	// 用新坐标更新客户端
	if s.client != nil {
		s.client.UpdateStationCoords(lat, lon)
	}

	s.logger.Info("已设置站点覆盖坐标",
		logger.Float64("latitude", lat),
		logger.Float64("longitude", lon))
}

// ClearStationOverride 移除覆盖坐标,恢复使用配置值
func (s *Service) ClearStationOverride() {
	s.overrideMutex.Lock()
	defer s.overrideMutex.Unlock()

	s.overrideLat = nil
	s.overrideLon = nil

	// 将客户端恢复为原始配置坐标
	if s.client != nil {
		s.client.UpdateStationCoords(s.stationLat, s.stationLon)
	}

	s.logger.Info("已清除站点覆盖坐标,使用配置值",
		logger.Float64("config_latitude", s.stationLat),
		logger.Float64("config_longitude", s.stationLon))
}

// GetEffectiveStationCoords 返回当前生效的站点坐标(覆盖值或配置值)
func (s *Service) GetEffectiveStationCoords() (lat, lon float64) {
	s.overrideMutex.RLock()
	defer s.overrideMutex.RUnlock()

	if s.overrideLat != nil && s.overrideLon != nil {
		return *s.overrideLat, *s.overrideLon
	}

	return s.stationLat, s.stationLon
}

// updateAircraftStatus 更新已不再活跃的飞行器的状态。
// 使用定向查询替代 GetAll() — 只获取确实需要状态更新的飞行器。
func (s *Service) updateAircraftStatus(activeAircraft map[string]bool) {
	now := time.Now().UTC()
	cutoff := now.Add(-s.signalLostTimeout)

	// 构建用于排除的活跃 hex 代码列表
	activeHexCodes := make([]string, 0, len(activeAircraft))
	for hex := range activeAircraft {
		activeHexCodes = append(activeHexCodes, hex)
	}

	// 定向查询:仅查询活跃、当前未广播且已过期的飞行器
	staleAircraft, err := s.storage.GetStaleActiveAircraft(activeHexCodes, cutoff)
	if err != nil {
		s.logger.Error("获取已过期活跃飞行器失败", logger.Error(err))
		return
	}

	var inactiveAircraft []*Aircraft

	for _, aircraft := range staleAircraft {
		aircraft.Status = "signal_lost"
		s.storage.Upsert(aircraft)

		// 仅跟踪有位置的飞行器以进行失联着陆检测
		hasPosition := aircraft.ADSB != nil && aircraft.ADSB.HasPosition()
		if hasPosition {
			inactiveAircraft = append(inactiveAircraft, aircraft)
		}

		timeSinceLastSeen := now.Sub(aircraft.LastSeen)
		s.logger.Info("飞行器状态已更新",
			logger.String("hex", aircraft.Hex),
			logger.String("flight", aircraft.Flight),
			logger.String("new_status", "signal_lost"),
			logger.Bool("on_ground", aircraft.OnGround),
			logger.Duration("time_since_last_seen", timeSinceLastSeen),
		)

		// 发送 WebSocket 消息(地面飞行器跳过)
		if s.wsServer != nil && !aircraft.OnGround {
			s.wsServer.Broadcast(&websocket.Message{
				Type: "status_update",
				Data: map[string]interface{}{
					"hex":                  aircraft.Hex,
					"flight":               aircraft.Flight,
					"new_status":           "signal_lost",
					"on_ground":            aircraft.OnGround,
					"time_since_last_seen": timeSinceLastSeen.Seconds(),
					"timestamp":            now.Format(time.RFC3339),
				},
			})
		}
	}

	// 检查失联着陆
	landingPhaseChanges := s.detectSignalLostLandings(inactiveAircraft)
	if len(landingPhaseChanges) > 0 {
		err := s.storage.InsertPhaseChangesBatch(landingPhaseChanges)
		if err != nil {
			s.logger.Error("插入失联着陆阶段失败", logger.Error(err))
		} else {
			s.sendImmediateGroundTransitionAlerts(landingPhaseChanges)
		}
	}
}

// detectGroundStateTransitions 检测立即起飞/着陆事件。
// 使用预获取的批量数据,避免单飞行器数据库查询。
func (s *Service) detectGroundStateTransitions(aircraft []*Aircraft, existingOnGround map[string]bool, currentPhases map[string]*PhaseChange, adsbTargetIDs map[string]*int) []PhaseChangeInsert {
	var immediatePhaseChanges []PhaseChangeInsert
	now := time.Now().UTC()

	for _, a := range aircraft {
		// 使用预获取的批量数据检查飞行器是否存在
		prevOnGround, found := existingOnGround[a.Hex]
		if !found {
			continue // 新飞行器 - 将由常规阶段检测处理
		}

		// 检查地面状态是否发生变化
		if prevOnGround != a.OnGround {
			var newPhase string
			var eventType string

			// 从预获取的批量数据中获取当前阶段以防止 T/O ↔ T/D 快速抖动
			currentPhase := currentPhases[a.Hex]

			if !prevOnGround && a.OnGround {
				// 飞行器先前在空中,现在在地面 = 着陆

				// 抗抖动:如果 T/O 是近期的,则阻止 T/O → T/D 切换
				if currentPhase != nil && currentPhase.Phase == "T/O" {
					timeSinceTakeoff := time.Since(currentPhase.Timestamp).Seconds()
					preservationThreshold := float64(s.flightPhasesConfig.PhasePreservationSeconds)
					if timeSinceTakeoff < preservationThreshold {
						s.logger.Warn("防止 T/O → T/D 快速抖动",
							logger.String("hex", a.Hex),
							logger.String("flight", a.Flight),
							logger.Float64("time_since_takeoff", timeSinceTakeoff),
							logger.Float64("threshold_seconds", preservationThreshold),
							logger.Float64("altitude", a.ADSB.AltBaro.Float64()),
						)
						continue // 跳过此次切换
					}
				}

				newPhase = "T/D"
				eventType = "landing"

				// 为使用中跑道检测记录着陆
				if s.trajectoryTracker != nil && a.ADSB != nil {
					lat, lon, ok := a.ADSB.Position()
					if ok {
						runwayInfo := DetectRunwayApproach(
							lat, lon, NumberOrZero(a.ADSB.Track),
							a.ADSB.AltBaro.Float64(), s.runwayData, s.flightPhasesConfig,
						)
						if runwayInfo != nil && runwayInfo.OnApproach {
							s.trajectoryTracker.RecordRunwayLanding(runwayInfo.RunwayID, a.Hex)
						}
					}
				}

				s.logger.Info("立即着陆已检测",
					logger.String("hex", a.Hex),
					logger.String("flight", a.Flight),
					logger.Bool("was_on_ground", prevOnGround),
					logger.Bool("now_on_ground", a.OnGround),
					logger.Float64("altitude", a.ADSB.AltBaro.Float64()),
					logger.Float64("ground_speed", NumberOrZero(a.ADSB.GS)),
				)

			} else if prevOnGround && !a.OnGround {
				// 飞行器先前在地面,现在在空中 = 起飞

				// 防止因不完整的 ADSB 数据导致假起飞:
				// 当一架飞行器首次出现且无/零高度时,会被标记为 on_ground=true。
				// 如果下一周期送达的真实高度显示飞行器远高于地面,
				// 那是数据正在变得可用 — 而不是实际起飞。跳过 T/O 并让
				// 轨迹跟踪器分配正确的阶段(例如 CRZ、ARR、UNK)。
				alt := a.ADSB.AltBaro.Float64()
				if alt > float64(s.flightPhasesConfig.DepartureAltitudeFt) {
					s.logger.Info("跳过假起飞 — 高度超过离场阈值(不完整数据正在变得可用)",
						logger.String("hex", a.Hex),
						logger.String("flight", a.Flight),
						logger.Float64("altitude", alt),
						logger.Float64("departure_threshold_ft", float64(s.flightPhasesConfig.DepartureAltitudeFt)),
					)
					continue
				}

				// 抗抖动:如果 T/D 是近期的,则阻止 T/D → T/O 切换
				if currentPhase != nil && currentPhase.Phase == "T/D" {
					timeSinceLanding := time.Since(currentPhase.Timestamp).Seconds()
					preservationThreshold := float64(s.flightPhasesConfig.PhasePreservationSeconds)
					if timeSinceLanding < preservationThreshold {
						s.logger.Warn("防止 T/D → T/O 快速抖动",
							logger.String("hex", a.Hex),
							logger.String("flight", a.Flight),
							logger.Float64("time_since_landing", timeSinceLanding),
							logger.Float64("threshold_seconds", preservationThreshold),
							logger.Float64("altitude", a.ADSB.AltBaro.Float64()),
						)
						continue // 跳过此次切换
					}
				}

				newPhase = "T/O"
				eventType = "takeoff"

				s.logger.Info("立即起飞已检测",
					logger.String("hex", a.Hex),
					logger.String("flight", a.Flight),
					logger.Bool("was_on_ground", prevOnGround),
					logger.Bool("now_on_ground", a.OnGround),
					logger.Float64("altitude", a.ADSB.AltBaro.Float64()),
					logger.Float64("ground_speed", NumberOrZero(a.ADSB.GS)),
				)
			}

			if newPhase != "" {
				// 使用预获取的 ADSB 目标 ID
				adsbId := adsbTargetIDs[a.Hex]

				immediatePhaseChanges = append(immediatePhaseChanges, PhaseChangeInsert{
					Hex:       a.Hex,
					Flight:    a.Flight,
					Phase:     newPhase,
					Timestamp: now,
					ADSBId:    adsbId,
					EventType: eventType,
				})
			}
		}
	}

	return immediatePhaseChanges
}

// detectSignalLostLandings checks for aircraft that lost signal near the airport
// and marks them as landed if they meet certain criteria
func (s *Service) detectSignalLostLandings(inactiveAircraft []*Aircraft) []PhaseChangeInsert {
	var landingPhaseChanges []PhaseChangeInsert

	// Check if signal lost landing detection is enabled
	if !s.flightPhasesConfig.SignalLostLandingEnabled {
		return landingPhaseChanges
	}

	now := time.Now().UTC()

	for _, aircraft := range inactiveAircraft {
		// Skip if already on ground or no ADSB data
		if aircraft.OnGround || aircraft.ADSB == nil {
			continue
		}

		// Check if aircraft was in approach phase
		currentPhase, err := s.storage.GetCurrentPhase(aircraft.Hex)
		if err != nil || currentPhase == nil {
			continue
		}

		// Check if the trajectory shows the aircraft was descending toward the station.
		// This is a stronger signal than phase alone — if the trajectory buffer confirms
		// a steady descent toward the airport, we can be confident this is a landing.
		trajectoryConfirms := false
		if s.trajectoryTracker != nil {
			trajectoryConfirms = s.trajectoryTracker.WasDescendingTowardStation(aircraft.Hex)
		}

		// Accept aircraft in APP phase, low altitude ARR, or trajectory-confirmed descent
		if currentPhase.Phase != "APP" &&
			!(currentPhase.Phase == "ARR" && aircraft.ADSB.AltBaro.Float64() < 2000) &&
			!trajectoryConfirms {
			continue
		}

		// Check proximity to airport
		lat, lon, hasPosition := aircraft.ADSB.Position()
		if !hasPosition {
			continue
		}
		distanceFromStation := MetersToNM(Haversine(
			lat, lon,
			s.stationLat, s.stationLon,
		))

		// If aircraft was close to airport and low altitude when signal lost
		if distanceFromStation <= s.flightPhasesConfig.AirportRangeNM &&
			aircraft.ADSB.AltBaro.Float64() < s.flightPhasesConfig.SignalLostLandingMaxAltFt {

			// Record for runway-in-use detection
			if s.trajectoryTracker != nil {
				runwayInfo := DetectRunwayApproach(
					lat, lon, NumberOrZero(aircraft.ADSB.Track),
					aircraft.ADSB.AltBaro.Float64(), s.runwayData, s.flightPhasesConfig,
				)
				if runwayInfo != nil && runwayInfo.OnApproach {
					s.trajectoryTracker.RecordRunwayLanding(runwayInfo.RunwayID, aircraft.Hex)
				}
			}

			// Mark as landed
			aircraft.OnGround = true
			s.storage.Upsert(aircraft)

			// Create T/D phase record
			adsbId, _ := s.storage.GetLatestADSBTargetID(aircraft.Hex)
			landingPhaseChanges = append(landingPhaseChanges, PhaseChangeInsert{
				Hex:       aircraft.Hex,
				Flight:    aircraft.Flight,
				Phase:     "T/D",
				Timestamp: now,
				ADSBId:    adsbId,
				EventType: "signal_lost_landing",
			})

			s.logger.Info("Signal lost aircraft marked as landed",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.Float64("last_altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.Float64("distance_from_airport", distanceFromStation),
			)
		}
	}

	return landingPhaseChanges
}

// processPhaseChangesBatch handles phase detection using pre-fetched batch data.
// Returns any new phase changes that were inserted (for downstream enrichment).
func (s *Service) processPhaseChangesBatch(aircraft []*Aircraft, immediatePhaseChanges []PhaseChangeInsert,
	currentPhases map[string]*PhaseChange, adsbTargetIDs map[string]*int, takeoffTimes map[string]*time.Time) []PhaseChangeInsert {
	if !s.flightPhasesConfig.Enabled {
		return nil
	}

	if len(aircraft) == 0 {
		return nil
	}

	// Create map of aircraft that just had immediate ground transitions
	immediateTransitions := make(map[string]string) // hex -> phase
	for _, change := range immediatePhaseChanges {
		immediateTransitions[change.Hex] = change.Phase
	}

	// Process each aircraft using pre-fetched batch data
	var phaseChanges []PhaseChangeInsert
	for _, a := range aircraft {
		// Skip aircraft that just had immediate ground transitions
		if _, hasImmediate := immediateTransitions[a.Hex]; hasImmediate {
			continue
		}

		currentPhase := currentPhases[a.Hex]
		newPhase := s.trajectoryTracker.DeterminePhase(a, currentPhase, takeoffTimes[a.Hex])

		// Apply phase stability rules and determine if change needed
		finalPhase, shouldInsert := s.evaluatePhaseChange(a, currentPhase, newPhase)

		if shouldInsert {
			phaseChanges = append(phaseChanges, PhaseChangeInsert{
				Hex:       a.Hex,
				Flight:    a.Flight,
				Phase:     finalPhase,
				Timestamp: time.Now().UTC(),
				ADSBId:    adsbTargetIDs[a.Hex],
			})
		}
	}

	// Batch insert all phase changes
	if len(phaseChanges) > 0 {
		err := s.storage.InsertPhaseChangesBatch(phaseChanges)
		if err != nil {
			s.logger.Error("Failed to insert phase changes batch", logger.Error(err))
			return nil
		}

		// Send WebSocket alerts and log changes
		s.sendPhaseChangeAlerts(phaseChanges, currentPhases)
	}

	return phaseChanges
}

// evaluatePhaseChange applies phase stability rules and determines if a phase change
// should be inserted into the database.
//
// The trajectory-based phase detection (TrajectoryTracker.DeterminePhase) already provides
// noise-resistant classification through trajectory window analysis, so airborne flapping
// prevention is no longer needed. This function handles only:
//
//  1. T/O preservation — Keep takeoff phase visible for PhasePreservationSeconds
//  2. T/D post-landing flow — Transition T/D → TAX or T/D → NEW based on ground speed
//  3. Ground phase protection — Prevent premature TAX→NEW and T/D→NEW transitions
//  4. Inactive aircraft timeout — Revert long-parked aircraft to NEW
func (s *Service) evaluatePhaseChange(aircraft *Aircraft, latestPhase *PhaseChange, newPhase string) (string, bool) {
	currentPhase := newPhase

	// ── T/D post-landing flow ────────────────────────────────────────
	// After landing, guide the aircraft through T/D → TAX → NEW gracefully.
	if latestPhase != nil && latestPhase.Phase == "T/D" {
		if currentPhase == "NEW" {
			groundSpeed := 0.0
			if aircraft.ADSB != nil {
				groundSpeed = NumberOrZero(aircraft.ADSB.GS)
			}
			if aircraft.OnGround && aircraft.ADSB != nil &&
				groundSpeed >= float64(s.flightPhasesConfig.TaxiingMinSpeedKts) &&
				groundSpeed <= float64(s.flightPhasesConfig.TaxiingMaxSpeedKts) {
				currentPhase = "TAX"
			} else {
				// Keep T/D visible for the preservation period
				timeSinceLanding := time.Since(latestPhase.Timestamp).Seconds()
				if timeSinceLanding < float64(s.flightPhasesConfig.PhasePreservationSeconds) {
					currentPhase = "T/D"
				} else if aircraft.ADSB != nil && groundSpeed >= float64(s.flightPhasesConfig.TaxiingMinSpeedKts) {
					currentPhase = "TAX"
				} else {
					currentPhase = "NEW"
				}
			}
		}
	}

	// ── T/O preservation ─────────────────────────────────────────────
	// Keep takeoff phase visible for at least PhasePreservationSeconds so
	// controllers can see it in the UI before it transitions to DEP.
	if latestPhase != nil && latestPhase.Phase == "T/O" {
		timeSinceTakeoff := time.Since(latestPhase.Timestamp).Seconds()
		if timeSinceTakeoff < float64(s.flightPhasesConfig.PhasePreservationSeconds) {
			currentPhase = "T/O"
		}
	}

	// ── Phase stability: suppress directional→UNK flapping ──────────
	// Directional airborne phases (CLB, DEP, ARR) shouldn't downgrade to
	// UNK just because a brief turn reverses the distance trend. Only allow
	// transitions to other meaningful phases, not to UNK.
	if latestPhase != nil && currentPhase == "UNK" {
		switch latestPhase.Phase {
		case "CLB", "DEP", "ARR":
			currentPhase = latestPhase.Phase
		}
	}

	// ── Determine if a phase change should be recorded ───────────────
	var shouldInsert bool

	if latestPhase == nil {
		// Brand new aircraft — record initial phase
		shouldInsert = true
	} else if latestPhase.Phase != currentPhase {
		// Phase changed — check for premature ground transitions
		if currentPhase == "NEW" && (latestPhase.Phase == "T/D" || latestPhase.Phase == "TAX") {
			timeSinceLastPhase := time.Since(latestPhase.Timestamp).Seconds()
			if timeSinceLastPhase < float64(s.flightPhasesConfig.PhaseTransitionTimeoutSeconds) {
				// Too soon — keep the current ground phase
				currentPhase = latestPhase.Phase
			} else {
				shouldInsert = true
			}
		} else {
			shouldInsert = true
		}
	} else {
		// Phase unchanged — check inactive aircraft timeout
		if latestPhase.Phase != "NEW" {
			timeSinceLastPhase := time.Since(latestPhase.Timestamp).Seconds()
			if timeSinceLastPhase > float64(s.flightPhasesConfig.PhaseChangeTimeoutSeconds) {
				currentPhase = "NEW"
				shouldInsert = true
			}
		}
	}

	return currentPhase, shouldInsert
}

// sendPhaseChangeAlerts sends WebSocket alerts for phase changes
func (s *Service) sendPhaseChangeAlerts(phaseChanges []PhaseChangeInsert, currentPhases map[string]*PhaseChange) {
	for _, change := range phaseChanges {
		aircraft, found := s.storage.GetByHex(change.Hex)
		if !found || aircraft == nil {
			continue
		}

		var previousPhase string
		if prevPhase := currentPhases[change.Hex]; prevPhase != nil {
			previousPhase = prevPhase.Phase
		}

		// Log the phase change with detailed aircraft data for debugging
		distanceFromStation := 0.0
		if lat, lon, hasPosition := aircraft.ADSB.Position(); hasPosition {
			distanceFromStation = MetersToNM(Haversine(lat, lon, s.stationLat, s.stationLon))
		}

		if previousPhase == "" {
			s.logger.Info("New aircraft phase detected",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.String("phase", change.Phase),
				logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.Float64("ground_speed", NumberOrZero(aircraft.ADSB.GS)),
				logger.Float64("vertical_rate", NumberOrZero(aircraft.ADSB.BaroRate)),
				logger.Float64("distance_from_station", distanceFromStation),
				logger.Bool("on_ground", aircraft.OnGround),
			)
		} else {
			s.logger.Info("Phase change detected",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.String("transition", previousPhase+" → "+change.Phase),
				logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.Float64("ground_speed", NumberOrZero(aircraft.ADSB.GS)),
				logger.Float64("vertical_rate", NumberOrZero(aircraft.ADSB.BaroRate)),
				logger.Float64("distance_from_station", distanceFromStation),
				logger.Bool("on_ground", aircraft.OnGround),
			)
		}

		// Send WebSocket message for phase change
		if s.wsServer != nil {
			// Create message data for phase change
			data := map[string]interface{}{
				"hex":        aircraft.Hex,
				"flight":     aircraft.Flight,
				"phase":      change.Phase,
				"prev_phase": previousPhase,
				"transition": previousPhase + " → " + change.Phase,
				"altitude":   aircraft.ADSB.AltBaro.Float64(),
				"on_ground":  aircraft.OnGround,
				"timestamp":  change.Timestamp.Format(time.RFC3339),
			}

			// Broadcast the phase change message
			s.wsServer.Broadcast(&websocket.Message{
				Type: "phase_change",
				Data: data,
			})
		}

		// Handle special takeoff/landing phases
		if change.Phase == "T/O" {
			s.logger.Info("Aircraft TOOK OFF",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.String("transition", previousPhase+" → T/O"),
				logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.Bool("on_ground", aircraft.OnGround),
			)

			// T/O phase change message is sent by the main phase change handler
		} else if change.Phase == "T/D" {
			s.logger.Info("Aircraft LANDED",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.String("transition", previousPhase+" → T/D"),
				logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.Bool("on_ground", aircraft.OnGround),
			)

			// T/D phase change message is sent by the main phase change handler
		}
	}
}

// sendImmediateGroundTransitionAlerts sends immediate WebSocket alerts for ground state transitions
func (s *Service) sendImmediateGroundTransitionAlerts(phaseChanges []PhaseChangeInsert) {
	for _, change := range phaseChanges {
		aircraft, found := s.storage.GetByHex(change.Hex)
		if !found || aircraft == nil {
			continue
		}

		// Get the previous phase for the transition message
		var previousPhase string
		phaseHistory, err := s.storage.GetPhaseHistory(change.Hex)
		if err == nil && len(phaseHistory) > 1 {
			// The first item is the current (just inserted), second is the previous
			previousPhase = phaseHistory[1].Phase
		}

		if change.Phase == "T/O" {
			s.logger.Info("Aircraft TOOK OFF (IMMEDIATE)",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.String("timestamp", change.Timestamp.Format(time.RFC3339)),
			)

			// Send phase change message first
			if s.wsServer != nil {
				// Send phase_change message
				phaseData := map[string]interface{}{
					"hex":        aircraft.Hex,
					"flight":     aircraft.Flight,
					"phase":      change.Phase,
					"prev_phase": previousPhase,
					"transition": previousPhase + " → " + change.Phase,
					"altitude":   aircraft.ADSB.AltBaro.Float64(),
					"on_ground":  aircraft.OnGround,
					"timestamp":  change.Timestamp.Format(time.RFC3339),
				}

				s.wsServer.Broadcast(&websocket.Message{
					Type: "phase_change",
					Data: phaseData,
				})

				// T/O phase change message already sent above
			}

		} else if change.Phase == "T/D" {
			s.logger.Info("Aircraft LANDED (IMMEDIATE)",
				logger.String("hex", aircraft.Hex),
				logger.String("flight", aircraft.Flight),
				logger.Float64("altitude", aircraft.ADSB.AltBaro.Float64()),
				logger.String("timestamp", change.Timestamp.Format(time.RFC3339)),
			)

			// Send phase change message first
			if s.wsServer != nil {
				// Send phase_change message
				phaseData := map[string]interface{}{
					"hex":        aircraft.Hex,
					"flight":     aircraft.Flight,
					"phase":      change.Phase,
					"prev_phase": previousPhase,
					"transition": previousPhase + " → " + change.Phase,
					"altitude":   aircraft.ADSB.AltBaro.Float64(),
					"on_ground":  aircraft.OnGround,
					"timestamp":  change.Timestamp.Format(time.RFC3339),
				}

				s.wsServer.Broadcast(&websocket.Message{
					Type: "phase_change",
					Data: phaseData,
				})

				// T/D phase change message already sent above
			}
		}
	}
}

// ProcessRawData processes raw ADS-B data into aircraft objects
func (s *Service) ProcessRawData(rawData *RawAircraftData) []*Aircraft {
	s.logger.Debug("Processing raw ADS-B data",
		logger.Int("aircraft_count", len(rawData.Aircraft)),
	)

	aircraft := make([]*Aircraft, 0, len(rawData.Aircraft))
	now := time.Now().UTC() // Ensure we use UTC time

	for _, raw := range rawData.Aircraft {
		// Skip aircraft without a hex identifier (unusable data)
		if raw.Hex == "" {
			continue
		}

		// Get the raw flight name and clean it
		flightRaw := raw.Flight
		flightName := strings.TrimSpace(CleanFlightName(flightRaw))

		// If flightName is empty but hex is available, try to derive tail number
		if flightName == "" && raw.Hex != "" {
			tailNumber, err := IcaoToTailNumber(raw.Hex) // Use exported function from atc_utils.go
			if err == nil && tailNumber != "" {
				flightName = tailNumber + "*" // Appended * to indicate derived tail number
				s.logger.Debug("Derived tail number from ICAO hex",
					logger.String("hex", raw.Hex),
					logger.String("tail_number", flightName))
			} else if err != nil {
				s.logger.Debug("Failed to derive tail number from ICAO hex",
					logger.String("hex", raw.Hex),
					logger.Error(err))
			}
		}

		// Determine airline from callsign only for valid flight numbers (3 letters + 1-4 numbers)
		var airlineName, airlineCountry string
		if len(flightName) >= 4 && len(flightName) <= 7 {
			// Check if the first 3 characters are letters
			firstThree := strings.ToUpper(flightName[:3])
			isAllLetters := true
			for _, c := range firstThree {
				if c < 'A' || c > 'Z' {
					isAllLetters = false
					break
				}
			}

			// Check if the remaining characters are digits (1-4 digits)
			remainingChars := flightName[3:]
			isAllDigits := true
			for _, c := range remainingChars {
				if c < '0' || c > '9' {
					isAllDigits = false
					break
				}
			}

			// Only lookup airline if it's a valid flight number (3 letters + 1-4 numbers)
			if isAllLetters && isAllDigits && len(remainingChars) >= 1 && len(remainingChars) <= 4 {
				icaoCode := firstThree
				if s.refService != nil {
					airlineName = s.refService.LookupAirline(icaoCode)
					airlineCountry = s.refService.LookupAirlineCountry(icaoCode)
				}
				s.logger.Debug("Detected valid flight number",
					logger.String("flight", flightName),
					logger.String("airline_code", icaoCode),
					logger.String("airline", airlineName))
			}
		}

		// Sensor validation and OnGround determination handled by fetchAndProcess using batch data

		aircraftStatus := "active" // Always set to active for aircraft in current ADSB data

		// Check if this is a simulated aircraft
		isSimulated := (s.simulationService != nil && s.simulationService.IsSimulated(raw.Hex)) || raw.Type == "sim"
		var simulationControls *SimulationControls

		if isSimulated {
			// For simulated aircraft, extract controls from the raw data type field
			// The simulation service will have already populated the ADSB data with current values
			simulationControls = &SimulationControls{
				TargetHeading:      NumberOrZero(raw.TrueHeading), // Use current heading as target
				TargetSpeed:        NumberOrZero(raw.TAS),         // Use current TAS as target
				TargetVerticalRate: NumberOrZero(raw.BaroRate),    // Use current vertical rate as target
			}
		}

		a := &Aircraft{
			Hex:                raw.Hex,
			Flight:             flightName,
			Airline:            airlineName,
			AirlineCountry:     airlineCountry,
			Status:             aircraftStatus,
			LastSeen:           now.Add(-time.Duration(NumberOrZero(raw.Seen)) * time.Second),
			ADSB:               &raw,
			IsSimulated:        isSimulated,
			SimulationControls: simulationControls,
		}

		a.ADSB.ATCDerived = computeATCDerivedMetrics(a.ADSB, a.Distance)

		// Trajectory-based predictions (hindcast + forecast)
		if s.trajectoryTracker != nil {
			// Forecast: trajectory-aware kinematic model (replaces naive PredictFuturePositions)
			if forecast := s.trajectoryTracker.GetForecast(raw.Hex); len(forecast) > 0 {
				a.Future = PredictionPointsToPositions(forecast)
			}
			// Hindcast: backward extrapolation before first ADS-B contact
			if hindcast := s.trajectoryTracker.GetHindcast(raw.Hex); len(hindcast) > 0 {
				a.Hindcast = PredictionPointsToPositions(hindcast)
			}
		}

		// Fallback to old prediction if trajectory forecast unavailable
		if len(a.Future) == 0 && raw.HasPosition() && raw.AltBaro.Float64() != 0 {
			lat, lon, _ := raw.Position()
			heading := NumberOrZero(raw.TrueHeading)
			if heading == 0 {
				heading = NumberOrZero(raw.Track)
			}
			if heading == 0 {
				heading = NumberOrZero(raw.MagHeading)
			}
			speed := NumberOrZero(raw.TAS)
			if speed == 0 {
				speed = NumberOrZero(raw.GS)
			}
			verticalRate := NumberOrZero(raw.BaroRate)
			if verticalRate == 0 {
				verticalRate = NumberOrZero(raw.GeomRate)
			}
			if heading != 0 && speed != 0 {
				magHeading := NumberOrZero(raw.MagHeading)
				if magHeading == 0 {
					magHeading = heading
				}
				a.Future = PredictFuturePositions(
					lat, lon, raw.AltBaro.Float64(),
					heading, magHeading, speed, verticalRate,
				)
			}
		}

		aircraft = append(aircraft, a)
	}

	s.logger.Debug("Processed ADS-B data",
		logger.Int("processed_count", len(aircraft)),
	)

	return aircraft
}
