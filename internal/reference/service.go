package reference

import (
	"fmt"
	"math"
	"strings"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/pkg/logger"
)

// Service 提供对所有参考数据(飞行器、航司、机场、跑道、导航台)的统一访问。
// 所有数据在启动时一次性加载,凡涉及位置的数据均会进行地理过滤。
type Service struct {
	logger *logger.Logger

	// 全局数据(无地理过滤)
	aircraftMap map[string]*AircraftInfo // key:大写 hex
	airlineMap  map[string]AirlineInfo   // key:ICAO 或 IATA 代码 → 航司信息

	// 在 display_range_nm 范围内经过地理过滤的数据
	airports   []*AirportInfo
	airportMap map[string]*AirportInfo // key:机场 ident(ICAO)
	runways    []*RunwayInfo
	navaids    []*NavaidInfo

	// 主场专属数据
	homeRunways          []*RunwayInfo
	homeRunwayData       adsb.RunwayData
	homeRunwayExtensions map[string]map[string][]RunwayExtensionPoint
}

// NewService 创建一个新的参考服务,在启动时加载所有 CSV 数据。
func NewService(cfg ServiceConfig, log *logger.Logger) (*Service, error) {
	s := &Service{
		logger:      log.Named("reference"),
		aircraftMap: make(map[string]*AircraftInfo),
		airlineMap:  make(map[string]AirlineInfo),
		airportMap:  make(map[string]*AirportInfo),
	}

	// 1. 加载 aircraft.csv(全局)
	if cfg.AircraftCSVPath != "" {
		m, err := loadAircraftCSV(cfg.AircraftCSVPath)
		if err != nil {
			s.logger.Warn("加载 aircraft.csv 失败: " + err.Error())
		} else {
			s.aircraftMap = m
			s.logger.Info("飞行器数据加载完成",
				logger.Int("count", len(m)),
				logger.String("path", cfg.AircraftCSVPath))
		}
	}

	// 2. 加载 airlines.dat(全局)
	if cfg.AirlinesDATPath != "" {
		m, err := loadAirlineDAT(cfg.AirlinesDATPath)
		if err != nil {
			s.logger.Warn("加载 airlines.dat 失败: " + err.Error())
		} else {
			s.airlineMap = m
			s.logger.Info("航司数据加载完成",
				logger.Int("count", len(m)),
				logger.String("path", cfg.AirlinesDATPath))
		}
	}

	// 3. 加载 airports.csv(地理过滤)
	if cfg.AirportsCSVPath != "" {
		airports, airportMap, err := loadAirportsCSV(cfg.AirportsCSVPath, cfg.StationLat, cfg.StationLon, cfg.DisplayRangeNM)
		if err != nil {
			s.logger.Warn("加载 airports.csv 失败: " + err.Error())
		} else {
			s.airports = airports
			s.airportMap = airportMap
			s.logger.Info("机场数据加载完成",
				logger.Int("total_in_range", len(airports)),
				logger.Float64("range_nm", cfg.DisplayRangeNM))
		}
	}

	// 4. 加载 airport-frequencies.csv(挂载到已过滤的机场)
	if cfg.FrequenciesCSVPath != "" && len(s.airportMap) > 0 {
		if err := loadFrequenciesCSV(cfg.FrequenciesCSVPath, s.airportMap); err != nil {
			s.logger.Warn("加载 airport-frequencies.csv 失败: " + err.Error())
		} else {
			freqCount := 0
			for _, ap := range s.airports {
				freqCount += len(ap.Frequencies)
			}
			s.logger.Info("机场频率数据加载完成",
				logger.Int("count", freqCount))
		}
	}

	// 5. 加载 runways.csv(地理过滤 + 主场)
	if cfg.RunwaysCSVPath != "" {
		all, home, err := loadRunwaysCSV(cfg.RunwaysCSVPath, s.airportMap, cfg.HomeAirportCode)
		if err != nil {
			s.logger.Warn("加载 runways.csv 失败: " + err.Error())
		} else {
			s.runways = all
			s.homeRunways = home
			s.logger.Info("跑道数据加载完成",
				logger.Int("total_in_range", len(all)),
				logger.Int("home_airport", len(home)),
				logger.String("home_code", cfg.HomeAirportCode))
		}
	}

	// 6. 加载 navaids.csv(地理过滤)
	if cfg.NavaidsCSVPath != "" {
		navs, err := loadNavaidsCSV(cfg.NavaidsCSVPath, cfg.StationLat, cfg.StationLon, cfg.DisplayRangeNM)
		if err != nil {
			s.logger.Warn("加载 navaids.csv 失败: " + err.Error())
		} else {
			s.navaids = navs
			s.logger.Info("导航台数据加载完成",
				logger.Int("count", len(navs)),
				logger.Float64("range_nm", cfg.DisplayRangeNM))
		}
	}

	// 7. 构建主场跑道数据(以兼容旧版的飞行阶段检测格式)
	s.buildHomeRunwayData(cfg.HomeAirportCode)
	s.buildHomeRunwayExtensions(cfg.ExtensionLengthNM)

	return s, nil
}

// --- 飞行器信息丰富化 ---

// LookupAircraft 通过 hex 代码(不区分大小写)检索飞行器信息。
func (s *Service) LookupAircraft(hex string) *AircraftInfo {
	return s.aircraftMap[strings.ToUpper(hex)]
}

// LookupAirline 通过 ICAO 或 IATA 代码检索航司名称。
func (s *Service) LookupAirline(code string) string {
	return s.airlineMap[code].Name
}

// LookupAirlineCountry 通过 ICAO 或 IATA 代码检索航司所属国家。
func (s *Service) LookupAirlineCountry(code string) string {
	return s.airlineMap[code].Country
}

// AircraftCount 返回数据库中的飞行器数量。
func (s *Service) AircraftCount() int {
	return len(s.aircraftMap)
}

// AirlineCount 返回航司代码映射的数量。
func (s *Service) AirlineCount() int {
	return len(s.airlineMap)
}

// --- 机场 ---

// GetAirports 返回配置显示范围内的所有机场。
func (s *Service) GetAirports() []*AirportInfo {
	if s.airports == nil {
		return []*AirportInfo{}
	}
	return s.airports
}

// GetAirportsOnly 返回显示范围内的机场(不含直升机场)。
func (s *Service) GetAirportsOnly() []*AirportInfo {
	result := make([]*AirportInfo, 0)
	for _, ap := range s.airports {
		if ap.Type != "heliport" {
			result = append(result, ap)
		}
	}
	return result
}

// GetHeliportsOnly 仅返回显示范围内的直升机场。
func (s *Service) GetHeliportsOnly() []*AirportInfo {
	result := make([]*AirportInfo, 0)
	for _, ap := range s.airports {
		if ap.Type == "heliport" {
			result = append(result, ap)
		}
	}
	return result
}

// GetAirport 通过 ICAO ident 返回单个机场,未找到时返回 nil。
func (s *Service) GetAirport(ident string) *AirportInfo {
	return s.airportMap[strings.ToUpper(ident)]
}

// --- 跑道 ---

// GetRunways 返回配置显示范围内的所有跑道。
func (s *Service) GetRunways() []*RunwayInfo {
	if s.runways == nil {
		return []*RunwayInfo{}
	}
	return s.runways
}

// GetHomeRunways 仅返回主场的跑道。
func (s *Service) GetHomeRunways() []*RunwayInfo {
	return s.homeRunways
}

// GetHomeRunwayData 返回兼容旧版的 RunwayData 结构,用于飞行阶段检测。
func (s *Service) GetHomeRunwayData() adsb.RunwayData {
	return s.homeRunwayData
}

// GetHomeRunwayExtensions 返回主场预先计算好的跑道延长点。
func (s *Service) GetHomeRunwayExtensions() map[string]map[string][]RunwayExtensionPoint {
	return s.homeRunwayExtensions
}

// --- 导航台 ---

// GetNavaids 返回配置显示范围内的所有导航台。
func (s *Service) GetNavaids() []*NavaidInfo {
	if s.navaids == nil {
		return []*NavaidInfo{}
	}
	return s.navaids
}

// GetNavaidsByIdent 返回所有匹配指定 ident 的导航台(可能存在多个,例如 VOR+DME 共址)。
func (s *Service) GetNavaidsByIdent(ident string) []*NavaidInfo {
	upper := strings.ToUpper(ident)
	var result []*NavaidInfo
	for _, n := range s.navaids {
		if strings.ToUpper(n.Ident) == upper {
			result = append(result, n)
		}
	}
	return result
}

// --- 内部构建函数 ---

// thresholdEntry 与 adsb.RunwayData.RunwayThresholds 中的匿名结构类型保持一致
type thresholdEntry = struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// buildHomeRunwayData 将主场跑道转换为 DetectRunwayApproach/DetectRunwayDeparture
// 所要求的 adsb.RunwayData 格式。
func (s *Service) buildHomeRunwayData(homeCode string) {
	s.homeRunwayData = adsb.RunwayData{
		Airport:          homeCode,
		RunwayThresholds: make(map[string]map[string]thresholdEntry),
	}

	for _, rwy := range s.homeRunways {
		if rwy.LEIdent == "" || rwy.HEIdent == "" {
			continue
		}
		// 飞行阶段检测要求两端坐标都有效
		if (rwy.LELatitude == 0 && rwy.LELongitude == 0) || (rwy.HELatitude == 0 && rwy.HELongitude == 0) {
			continue
		}

		pairKey := fmt.Sprintf("%s-%s", rwy.LEIdent, rwy.HEIdent)
		thresholds := make(map[string]thresholdEntry)
		thresholds[rwy.LEIdent] = thresholdEntry{
			Latitude: rwy.LELatitude, Longitude: rwy.LELongitude,
		}
		thresholds[rwy.HEIdent] = thresholdEntry{
			Latitude: rwy.HELatitude, Longitude: rwy.HELongitude,
		}
		s.homeRunwayData.RunwayThresholds[pairKey] = thresholds
	}
}

// buildHomeRunwayExtensions 为主场预先计算跑道延长点。
func (s *Service) buildHomeRunwayExtensions(extensionLengthNM float64) {
	if extensionLengthNM <= 0 {
		extensionLengthNM = 10.0
	}

	s.homeRunwayExtensions = make(map[string]map[string][]RunwayExtensionPoint)

	for pairKey, thresholds := range s.homeRunwayData.RunwayThresholds {
		s.homeRunwayExtensions[pairKey] = make(map[string][]RunwayExtensionPoint)

		for endID, threshold := range thresholds {
			// 找到对侧端
			var opposite thresholdEntry
			for otherID, otherThreshold := range thresholds {
				if otherID != endID {
					opposite = otherThreshold
					break
				}
			}

			// 由当前端指向对侧端的方位角
			bearing := calculateBearing(
				threshold.Latitude, threshold.Longitude,
				opposite.Latitude, opposite.Longitude,
			)
			// 延长方向为反向
			oppositeBearing := math.Mod(bearing+180, 360)

			points := []RunwayExtensionPoint{
				{Latitude: threshold.Latitude, Longitude: threshold.Longitude, Distance: 0},
			}

			for d := 1.0; d <= extensionLengthNM; d += 1.0 {
				lat, lon := calculateDestinationPoint(
					threshold.Latitude, threshold.Longitude,
					oppositeBearing, d,
				)
				points = append(points, RunwayExtensionPoint{
					Latitude: lat, Longitude: lon, Distance: d,
				})
			}

			s.homeRunwayExtensions[pairKey][endID] = points
		}
	}
}
