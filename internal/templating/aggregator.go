package templating

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/frequencies"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/weather"
	"github.com/yegors/co-atc/pkg/logger"
)

const activeRunwayProbabilityThreshold = 0.15
const defaultTranscriptionHistorySeconds = 600

// DataAggregator 收集并格式化用于模板渲染的空域数据
type DataAggregator struct {
	adsbService          *adsb.Service
	weatherService       *weather.Service
	transcriptionStorage *sqlite.TranscriptionStorage
	frequencyService     *frequencies.Service
	config               *config.Config
	logger               *logger.Logger
}

// NewDataAggregator 创建一个新的数据聚合器
func NewDataAggregator(
	adsbService *adsb.Service,
	weatherService *weather.Service,
	transcriptionStorage *sqlite.TranscriptionStorage,
	frequencyService *frequencies.Service,
	config *config.Config,
	logger *logger.Logger,
) *DataAggregator {
	return &DataAggregator{
		adsbService:          adsbService,
		weatherService:       weatherService,
		transcriptionStorage: transcriptionStorage,
		frequencyService:     frequencyService,
		config:               config,
		logger:               logger.Named("template-aggregator"),
	}
}

// GetTemplateContext 聚合所有当前空域数据用于模板渲染
func (da *DataAggregator) GetTemplateContext(opts FormattingOptions) (*TemplateContext, error) {
	// 如果 ATC 聊天可用,则使用配置值覆盖最大飞行器数
	maxAircraft := opts.MaxAircraft
	if opts.IncludeTranscriptionHistory && da.config.ATCChat.MaxContextAircraft > 0 {
		maxAircraft = da.config.ATCChat.MaxContextAircraft
	}

	da.logger.Debug("正在聚合模板上下文",
		logger.Int("max_aircraft", maxAircraft),
		logger.Int("config_max_aircraft", da.config.ATCChat.MaxContextAircraft),
		logger.Bool("include_weather", opts.IncludeWeather),
		logger.Bool("include_runways", opts.IncludeRunways),
		logger.Bool("include_transcription_history", opts.IncludeTranscriptionHistory))

	context := &TemplateContext{
		Timestamp: time.Now().UTC(),
		Airport:   da.getAirportInfo(),
	}

	// 获取飞行器数据
	aircraft, err := da.getAircraftData(maxAircraft)
	if err != nil {
		da.logger.Error("获取飞行器数据失败", logger.Error(err))
		// 继续使用空的飞行器列表,而不是完全失败
		aircraft = []*adsb.Aircraft{}
	}
	context.Aircraft = aircraft

	// 如果请求,获取气象数据
	if opts.IncludeWeather {
		weatherData, err := da.getWeatherData()
		if err != nil {
			da.logger.Error("获取气象数据失败", logger.Error(err))
			// 继续使用 nil weather,而不是完全失败
		}
		context.Weather = weatherData
	}

	// 如果请求,获取跑道数据
	if opts.IncludeRunways {
		runways, err := da.getRunwayData()
		if err != nil {
			da.logger.Error("获取跑道数据失败", logger.Error(err))
			// 继续使用空跑道,而不是完全失败
			runways = []RunwayInfo{}
		}
		context.Runways = runways
		context.ActiveRunways = da.getActiveRunwayScores(2)
	}

	// 如果请求,获取最近通信(仅用于 ATC 聊天)
	if opts.IncludeTranscriptionHistory {
		communications, err := da.getRecentCommunications()
		if err != nil {
			da.logger.Error("获取最近通信失败", logger.Error(err))
			// 继续使用空通信,而不是完全失败
			communications = []TranscriptionSummary{}
		}
		context.TranscriptionHistory = communications
	}

	da.logger.Debug("模板上下文已聚合",
		logger.Int("aircraft_count", len(context.Aircraft)),
		logger.Int("runway_count", len(context.Runways)),
		logger.Int("communication_count", len(context.TranscriptionHistory)))

	return context, nil
}

func (da *DataAggregator) getActiveRunwayScores(maxRunways int) []adsb.RunwayScore {
	if da.adsbService == nil || maxRunways <= 0 {
		return nil
	}

	scores := da.adsbService.GetRunwayInUseScores(10)
	if len(scores) == 0 {
		return nil
	}

	detected := make([]adsb.RunwayScore, 0, maxRunways)
	for _, score := range scores {
		if score.Probability < activeRunwayProbabilityThreshold {
			continue
		}
		detected = append(detected, score)
		if len(detected) >= maxRunways {
			break
		}
	}

	return detected
}

// getAircraftData 获取带距离过滤的飞行器数据
func (da *DataAggregator) getAircraftData(maxAircraft int) ([]*adsb.Aircraft, error) {
	// 从 ADSB 服务获取飞行器
	allAircraft := da.adsbService.GetAllAircraft()

	if len(allAircraft) == 0 {
		return []*adsb.Aircraft{}, nil
	}

	// 第一步过滤: 仅包含活动的飞行器(排除 signal_lost、stale 等)
	var activeAircraft []*adsb.Aircraft
	for _, ac := range allAircraft {
		if ac.Status == "active" {
			activeAircraft = append(activeAircraft, ac)
		}
	}

	da.logger.Debug("按状态过滤飞行器",
		logger.Int("total_aircraft", len(allAircraft)),
		logger.Int("active_aircraft", len(activeAircraft)))

	if len(activeAircraft) == 0 {
		return []*adsb.Aircraft{}, nil
	}

	// 按距机场距离过滤
	airport := da.getAirportInfo()
	var aircraft []*adsb.Aircraft
	if len(airport.Coordinates) >= 2 {
		radius := da.config.Station.AirportRangeNM

		for _, ac := range activeAircraft {
			if ac.ADSB != nil && ac.ADSB.HasPosition() {
				lat, lon, _ := ac.ADSB.Position()
				distance := da.calculateDistance(lat, lon, airport.Coordinates[0], airport.Coordinates[1])

				// 如果在半径内或在空中(保留所有空中流量),则包含
				if distance <= radius || !ac.OnGround {
					ac.Distance = &distance
					adsb.AttachATCDerivedMetrics(ac)
					aircraft = append(aircraft, ac)
				}
			}
		}
	} else {
		// 如果没有机场坐标,则仅使用活动飞行器
		aircraft = activeAircraft
	}

	da.logger.Debug("按距离过滤飞行器",
		logger.Int("active_aircraft", len(activeAircraft)),
		logger.Int("filtered_aircraft", len(aircraft)))

	// 限制飞行器数量
	if len(aircraft) > maxAircraft {
		aircraft = aircraft[:maxAircraft]
	}

	return aircraft, nil
}

// getWeatherData 检索当前气象信息
func (da *DataAggregator) getWeatherData() (*weather.WeatherData, error) {
	if da.weatherService == nil {
		return nil, fmt.Errorf("气象服务不可用")
	}

	weatherData := da.weatherService.GetWeatherData()
	if weatherData == nil {
		return nil, fmt.Errorf("没有可用的气象数据")
	}

	return weatherData, nil
}

// getRunwayData 从启动时加载的参考数据中检索跑道配置
func (da *DataAggregator) getRunwayData() ([]RunwayInfo, error) {
	if da.adsbService == nil {
		return nil, fmt.Errorf("adsb 服务不可用")
	}

	rwData := da.adsbService.GetRunwayData()
	if len(rwData.RunwayThresholds) == 0 {
		return []RunwayInfo{}, nil
	}

	// 从阈值数据中提取唯一的跑道端点标识符
	var runways []RunwayInfo
	for _, thresholds := range rwData.RunwayThresholds {
		for endID := range thresholds {
			runways = append(runways, RunwayInfo{
				Name:       endID,
				Active:     true,
				Operations: []string{"departure", "arrival"},
			})
		}
	}
	return runways, nil
}

// getRecentCommunications 检索最近的无线电通信
func (da *DataAggregator) getRecentCommunications() ([]TranscriptionSummary, error) {
	if da.transcriptionStorage == nil {
		return []TranscriptionSummary{}, nil
	}

	// 获取最近的转写(默认: 最近 10 分钟)
	timeWindowSeconds := defaultTranscriptionHistorySeconds
	if da.config.ATCChat.TranscriptionHistorySeconds > 0 {
		timeWindowSeconds = da.config.ATCChat.TranscriptionHistorySeconds
	}

	since := time.Now().UTC().Add(-time.Duration(timeWindowSeconds) * time.Second)
	endTime := time.Now().UTC()

	transcriptions, err := da.transcriptionStorage.GetTranscriptionsByTimeRange(since, endTime, 100, 0)
	if err != nil {
		return nil, fmt.Errorf("获取最近转写失败: %w", err)
	}

	// 转换为 TranscriptionSummary 格式
	var communications []TranscriptionSummary
	for _, t := range transcriptions {
		content := strings.TrimSpace(t.ContentProcessed)
		if content == "" {
			content = strings.TrimSpace(t.Content)
		}

		if content == "" {
			continue
		}

		// 获取频率名称
		frequencyName := t.FrequencyID
		if da.frequencyService != nil {
			if freq, ok := da.frequencyService.GetFrequencyByID(t.FrequencyID); ok {
				frequencyName = freq.Name
			}
		}

		communications = append(communications, TranscriptionSummary{
			Timestamp: t.CreatedAt,
			Frequency: frequencyName,
			Content:   content,
			Speaker:   t.SpeakerType,
			Callsign:  t.Callsign,
		})
	}

	return communications, nil
}

// getAirportInfo 从配置返回机场信息
func (da *DataAggregator) getAirportInfo() AirportInfo {
	// 如果配置中没有,则从代码生成机场名称
	airportName := da.config.Station.AirportCode
	if da.config.Station.AirportCode != "" {
		airportName = "Airport " + da.config.Station.AirportCode
	}

	return AirportInfo{
		Code:        da.config.Station.AirportCode,
		Name:        airportName,
		Coordinates: []float64{da.config.Station.Latitude, da.config.Station.Longitude},
		ElevationFt: int(da.config.Station.ElevationFeet),
	}
}

// calculateDistance 使用 Haversine 公式计算两点之间的距离
func (da *DataAggregator) calculateDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 3440.07 // 地球半径,单位为海里

	// 度数转换为弧度
	lat1Rad := lat1 * math.Pi / 180
	lon1Rad := lon1 * math.Pi / 180
	lat2Rad := lat2 * math.Pi / 180
	lon2Rad := lon2 * math.Pi / 180

	// Haversine 公式
	dLat := lat2Rad - lat1Rad
	dLon := lon2Rad - lon1Rad

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return R * c
}
