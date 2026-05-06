package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/atcchat"
	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/frequencies"
	"github.com/yegors/co-atc/internal/reference"
	"github.com/yegors/co-atc/internal/simulation"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/weather"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// Handler 包含 API 处理器
type Handler struct {
	adsbService          *adsb.Service
	frequenciesService   *frequencies.Service
	weatherService       *weather.Service
	atcChatService       *atcchat.Service
	simulationService    *simulation.Service
	refService           *reference.Service
	config               *config.Config
	logger               *logger.Logger
	wsServer             *websocket.Server
	transcriptionStorage *sqlite.TranscriptionStorage
	clearanceStorage     *sqlite.ClearanceStorage
}

// NewHandler 创建一个新的 API 处理器
func NewHandler(adsbService *adsb.Service, frequenciesService *frequencies.Service, weatherService *weather.Service, atcChatService *atcchat.Service, simulationService *simulation.Service, refService *reference.Service, config *config.Config, logger *logger.Logger, wsServer *websocket.Server, transcriptionStorage *sqlite.TranscriptionStorage, clearanceStorage *sqlite.ClearanceStorage) *Handler {
	return &Handler{
		adsbService:          adsbService,
		frequenciesService:   frequenciesService,
		weatherService:       weatherService,
		atcChatService:       atcChatService,
		simulationService:    simulationService,
		refService:           refService,
		config:               config,
		logger:               logger.Named("api-handler"),
		wsServer:             wsServer,
		transcriptionStorage: transcriptionStorage,
		clearanceStorage:     clearanceStorage,
	}
}

// GetAllAircraft 返回所有飞行器
func (h *Handler) GetAllAircraft(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h.logger.Debug("正在启动 GetAllAircraft API 调用")

	// 解析查询参数
	minAltitude, maxAltitude, callsign, status, lastSeenMinutes,
		tookOffAfter, tookOffBefore, landedAfter, landedBefore, distanceNM,
		refLat, refLon, refHex, refFlight, excludeOtherAirportsGrounded, simple := parseAircraftFilters(r)

	// 获取飞行器数据
	dataFetchStart := time.Now()
	var aircraft []*adsb.Aircraft
	// 跟踪是否使用了数据库级别的 last_seen 过滤
	usedDBLastSeenFilter := false

	if minAltitude > 0 || maxAltitude < 60000 || len(status) > 0 ||
		tookOffAfter != nil || tookOffBefore != nil ||
		landedAfter != nil || landedBefore != nil {
		// 使用带日期过滤器的增强 GetFiltered 方法
		aircraft = h.adsbService.GetFilteredAircraft(
			minAltitude, maxAltitude,
			status,
			tookOffAfter, tookOffBefore, landedAfter, landedBefore,
		)
	} else if simple {
		// 对简单 API 使用最小模式 - 跳过阶段历史和日期查询
		aircraft = h.adsbService.GetAllAircraftMinimal(lastSeenMinutes)
		usedDBLastSeenFilter = lastSeenMinutes > 0
	} else if lastSeenMinutes > 0 {
		// 对 last_seen 使用数据库级别过滤 - 对大型数据库速度快得多
		aircraft = h.adsbService.GetAllAircraftWithLastSeenFilter(lastSeenMinutes)
		usedDBLastSeenFilter = true
	} else {
		aircraft = h.adsbService.GetAllAircraft()
	}

	dataFetchDuration := time.Since(dataFetchStart)
	h.logger.Debug("飞行器数据获取已完成",
		logger.Duration("duration", dataFetchDuration),
		logger.Int("aircraft_count", len(aircraft)),
		logger.Bool("used_db_last_seen_filter", usedDBLastSeenFilter))

	// 如果提供了呼号则按呼号过滤
	if callsign != "" {
		filtered := make([]*adsb.Aircraft, 0)
		for _, a := range aircraft {
			if strings.Contains(strings.ToUpper(a.Flight), strings.ToUpper(callsign)) {
				filtered = append(filtered, a)
			}
		}
		aircraft = filtered
	}

	// 如果提供了最后可见时间则进行过滤(仅当尚未在数据库级别过滤时需要)
	if lastSeenMinutes > 0 && !usedDBLastSeenFilter {
		now := time.Now().UTC() // 截止时间使用 UTC
		cutoffTime := now.Add(-time.Duration(lastSeenMinutes) * time.Minute)

		filtered := make([]*adsb.Aircraft, 0)
		for _, a := range aircraft {
			if a.LastSeen.After(cutoffTime) {
				filtered = append(filtered, a)
			}
		}
		aircraft = filtered
	}

	// 如果提供了距离则应用距离过滤器
	if distanceNM > 0 {
		var refLatitude, refLongitude float64
		var refHeading, refAltitude float64
		var err error
		var refType string
		var refAircraft *adsb.Aircraft

		// 确定要使用哪个参考(按优先顺序)
		if refLat != 0 && refLon != 0 {
			// 使用提供的坐标
			refLatitude, refLongitude = refLat, refLon
			refType = "coordinates"
			err = nil
		} else if refHex != "" {
			// 使用飞行器 hex 码
			refAircraft, err = h.getRefAircraft(refHex)
			if err == nil && refAircraft != nil && refAircraft.ADSB != nil {
				if lat, lon, ok := refAircraft.ADSB.Position(); ok {
					refLatitude = lat
					refLongitude = lon
				}
				refHeading = adsb.NumberOrZero(refAircraft.ADSB.TrueHeading)
				if refHeading == 0 {
					refHeading = adsb.NumberOrZero(refAircraft.ADSB.Track) // 如果真航向不可用,则使用航迹
				}
				refAltitude = refAircraft.ADSB.AltBaro.Float64()
			}
			refType = "hex"
		} else if refFlight != "" {
			// 使用航班号
			refLatitude, refLongitude, err = h.getFlightCoordinates(refFlight)
			refType = "flight"
		} else {
			// 未提供有效参考
			err = fmt.Errorf("未提供有效的参考坐标")
			refType = "none"
		}

		if err == nil {
			filtered := make([]*adsb.Aircraft, 0)
			for _, a := range aircraft {
				// 跳过没有位置数据的飞行器
				if a.ADSB == nil || !a.ADSB.HasPosition() {
					continue
				}
				lat, lon, _ := a.ADSB.Position()

				// 邻近查询时跳过地面飞行器
				if a.OnGround {
					continue
				}

				// 跳过参考飞行器本身
				if refHex != "" && a.Hex == refHex {
					continue
				}

				// 邻近查询时仅包括活跃的飞行器
				if a.Status != "active" {
					continue
				}

				// 计算距离
				distMeters := adsb.Haversine(lat, lon, refLatitude, refLongitude)
				distNM := adsb.MetersToNM(distMeters)
				distNM = math.Round(distNM*10) / 10 // 四舍五入到 1 位小数

				// 如果在范围内则添加到过滤列表
				if distNM <= distanceNM {
					// 对于邻近查询,我们需要区分:
					// 1. 距站点的距离(常规距离字段)
					// 2. 距参考飞行器的距离(相对距离字段)

					// 为每个飞行器计算距站点的距离
					if a.ADSB != nil && a.ADSB.HasPosition() {
						stationDistMeters := adsb.Haversine(lat, lon, h.config.Station.Latitude, h.config.Station.Longitude)
						stationDistNM := adsb.MetersToNM(stationDistMeters)
						stationDistNM = math.Round(stationDistNM*10) / 10 // 四舍五入到 1 位小数
						a.Distance = &stationDistNM
					}

					// 存储计算出的相对距离
					a.RelativeDistance = &distNM

					// 如果我们有带航向的参考飞行器,则计算相对方位
					if refAircraft != nil && refHeading > 0 {
						bearing := adsb.CalculateRelativeBearing(
							refLatitude, refLongitude, refHeading,
							lat, lon)
						a.RelativeBearing = &bearing

						// 计算相对高度
						if refAltitude > 0 && a.ADSB.AltBaro.Float64() > 0 {
							relAlt := a.ADSB.AltBaro.Float64() - refAltitude
							a.RelativeAlt = &relAlt
						}
					}

					filtered = append(filtered, a)
				}
			}

			// 按相对距离对飞行器进行排序(升序)
			sort.Slice(filtered, func(i, j int) bool {
				// 处理 nil 情况(不应发生,但以防万一)
				if filtered[i].RelativeDistance == nil {
					return false
				}
				if filtered[j].RelativeDistance == nil {
					return true
				}
				return *filtered[i].RelativeDistance < *filtered[j].RelativeDistance
			})

			aircraft = filtered
		} else {
			h.logger.Error("解析参考坐标失败",
				logger.Error(err),
				logger.String("reference_type", refType),
				logger.String("ref_hex", refHex),
				logger.String("ref_flight", refFlight))
		}
	}

	// 如果请求,应用 exclude_other_airports_grounded 过滤器
	if excludeOtherAirportsGrounded {
		filtered := make([]*adsb.Aircraft, 0)
		airportRangeNM := h.config.Station.AirportRangeNM
		if airportRangeNM == 0 {
			airportRangeNM = 5.0 // 如果未配置则默认为 5.0 海里
		}

		for _, a := range aircraft {
			// 包含所有不在地面上的飞行器,或机场范围内的地面飞行器
			if !a.OnGround {
				filtered = append(filtered, a)
			} else if a.ADSB != nil && a.ADSB.HasPosition() {
				lat, lon, _ := a.ADSB.Position()
				// 为地面飞行器计算距站点的距离
				distMeters := adsb.Haversine(lat, lon, h.config.Station.Latitude, h.config.Station.Longitude)
				distNM := adsb.MetersToNM(distMeters)
				if distNM <= airportRangeNM {
					filtered = append(filtered, a)
				}
			}
		}
		aircraft = filtered
	}

	// 用位置历史中最后一个非零值更新零值
	for _, a := range aircraft {
		updateZeroValuesFromHistory(a)

		// 为每个飞行器计算距站点的距离
		if a.ADSB != nil && a.ADSB.HasPosition() {
			lat, lon, _ := a.ADSB.Position()
			distMeters := adsb.Haversine(lat, lon, h.config.Station.Latitude, h.config.Station.Longitude)
			distNM := adsb.MetersToNM(distMeters)
			distNM = math.Round(distNM*10) / 10 // 四舍五入到 1 位小数
			a.Distance = &distNM
		}

		// 检查是否为邻近查询(带 distance_nm 的 ref_hex 或 ref_lat/ref_lon)
		isProximityQuery := (refHex != "" || (refLat != 0 && refLon != 0)) && distanceNM > 0

		// 对于邻近查询,不包含历史数据以减小负载大小
		if isProximityQuery {
			a.History = nil
		}

		// Future 数组现在由预测算法填充
		adsb.AttachATCDerivedMetrics(a)
	}

	// 按地面/空中和活跃/总计计算计数
	groundActive := 0
	groundTotal := 0
	airActive := 0
	airTotal := 0

	for _, a := range aircraft {
		if a.OnGround {
			// 地面飞行器
			groundTotal++
			if a.Status == "active" {
				groundActive++
			}
		} else {
			// 空中飞行器
			airTotal++
			if a.Status == "active" {
				airActive++
			}
		}
	}

	// 填充每个飞行器的放行许可
	for _, aircraft := range aircraft {
		clearances, err := h.clearanceStorage.GetClearancesByCallsign(aircraft.Flight, 10) // 最近 10 个放行许可
		if err != nil {
			h.logger.Error("获取飞行器的放行许可失败",
				logger.String("callsign", aircraft.Flight),
				logger.Error(err))
			continue
		}

		// 转换为 API 格式
		aircraft.Clearances = h.convertClearancesToAPIFormat(clearances)
	}

	// 如果 simple=1 则返回简化响应
	if simple {
		simpleAircraft := make([]*adsb.AircraftSimple, 0, len(aircraft))
		for _, a := range aircraft {
			airlineName := strings.TrimSpace(a.Airline)
			if airlineName == "" && h.refService != nil {
				flight := strings.TrimSpace(strings.ToUpper(a.Flight))
				if len(flight) >= 3 {
					icaoCode := flight[:3]
					if icaoCode[0] >= 'A' && icaoCode[0] <= 'Z' &&
						icaoCode[1] >= 'A' && icaoCode[1] <= 'Z' &&
						icaoCode[2] >= 'A' && icaoCode[2] <= 'Z' {
						if resolved := strings.TrimSpace(h.refService.LookupAirline(icaoCode)); resolved != "" {
							airlineName = resolved
						}
					}
				}
			}

			sa := &adsb.AircraftSimple{
				Hex:      a.Hex,
				Callsign: a.Flight,
				Airline:  airlineName,
				Distance: a.Distance,
				Status:   a.Status,
			}
			// 如果可用则添加 BSDB 数据
			if a.BSDB != nil {
				sa.Registration = a.BSDB.Registration
				sa.AircraftType = a.BSDB.ICAOTypeCode
				sa.Manufacturer = a.BSDB.Manufacturer
				sa.RegisteredOwners = a.BSDB.RegisteredOwners
			}
			// 如果可用则添加 ADSB 数据
			if a.ADSB != nil {
				sa.Lat = a.ADSB.Lat
				sa.Lon = a.ADSB.Lon
				sa.AltBaro = math.Round(a.ADSB.AltBaro.Float64()/100) * 100
				if a.ADSB.GS != nil {
					v := math.Round(*a.ADSB.GS)
					sa.GroundSpeed = &v
				}
				if a.ADSB.TAS != nil {
					v := math.Round(*a.ADSB.TAS)
					sa.TrueAirspeed = &v
				}
				if a.ADSB.Track != nil {
					v := math.Round(*a.ADSB.Track)
					sa.Track = &v
				}
				if a.ADSB.MagHeading != nil {
					v := math.Round(*a.ADSB.MagHeading)
					sa.MagHeading = &v
				}
				if a.ADSB.BaroRate != nil {
					v := math.Round(*a.ADSB.BaroRate/100) * 100
					sa.VerticalRate = &v
				}
				sa.Squawk = a.ADSB.Squawk
				sa.Category = a.ADSB.Category
				// 如果 BSDB 类型不可用,使用 ADSB 类型
				if sa.AircraftType == "" {
					sa.AircraftType = a.ADSB.AircraftType
				}
				if sa.Registration == "" {
					sa.Registration = a.ADSB.Registration
				}
			}
			// 如果可用则添加当前阶段
			if a.Phase != nil && len(a.Phase.Current) > 0 {
				sa.Phase = a.Phase.Current[0].Phase
			}
			simpleAircraft = append(simpleAircraft, sa)
		}

		simpleResponse := adsb.AircraftSimpleResponse{
			Timestamp: time.Now().UTC(),
			Count:     len(simpleAircraft),
			Aircraft:  simpleAircraft,
		}
		WriteJSON(w, http.StatusOK, simpleResponse)

		totalDuration := time.Since(start)
		h.logger.Debug("GetAllAircraft API 调用已完成(简单模式)",
			logger.Duration("total_duration", totalDuration),
			logger.Int("final_aircraft_count", len(simpleAircraft)))
		return
	}

	// 创建响应
	response := adsb.AircraftResponse{
		Timestamp: time.Now().UTC(), // 响应时间戳使用 UTC
		Count:     len(aircraft),
		Counts: adsb.AircraftCounts{
			GroundActive: groundActive,
			GroundTotal:  groundTotal,
			AirActive:    airActive,
			AirTotal:     airTotal,
		},
		Aircraft: aircraft,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)

	totalDuration := time.Since(start)
	h.logger.Debug("GetAllAircraft API 调用已完成",
		logger.Duration("total_duration", totalDuration),
		logger.Int("final_aircraft_count", len(aircraft)))
}

// GetAircraftByHex 通过 hex ID 返回飞行器
func (h *Handler) GetAircraftByHex(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取 hex ID
	hex := chi.URLParam(r, "id")
	if hex == "" {
		http.Error(w, "Missing aircraft ID", http.StatusBadRequest)
		return
	}

	// 获取飞行器数据
	aircraft, found := h.adsbService.GetAircraftByHex(hex)
	if !found {
		http.Error(w, "Aircraft not found", http.StatusNotFound)
		return
	}

	// 用位置历史中最后一个非零值更新零值
	updateZeroValuesFromHistory(aircraft)

	// 计算距站点的距离
	if aircraft.ADSB != nil && aircraft.ADSB.HasPosition() {
		lat, lon, _ := aircraft.ADSB.Position()
		distMeters := haversine(lat, lon, h.config.Station.Latitude, h.config.Station.Longitude)
		distNM := math.Round(distMeters/1852.0*10) / 10 // 将米转换为海里并四舍五入到 1 位小数
		aircraft.Distance = &distNM
	}

	adsb.AttachATCDerivedMetrics(aircraft)

	// 写入响应
	WriteJSON(w, http.StatusOK, aircraft)
}

// positionDedupHeading 返回最佳可用航向(磁→航迹→真航向优先级)。
// 如果没有可用航向则返回 -1。
func positionDedupHeading(p adsb.Position) float64 {
	if p.MagHeading != nil {
		return *p.MagHeading
	}
	if p.Track != nil {
		return *p.Track
	}
	if p.TrueHeading != nil {
		return *p.TrueHeading
	}
	return -1
}

// positionsMatchForDedup 如果两个位置在容差为 1 的范围内具有
// 实际上相同的显示值(高度四舍五入到 100 英尺、航向、TAS、GS)
// 则返回 true。距离除外。
func positionsMatchForDedup(a, b adsb.Position) bool {
	altA := math.Round(adsb.NumberOrZero(a.Altitude)/100) * 100
	altB := math.Round(adsb.NumberOrZero(b.Altitude)/100) * 100
	if a.Altitude != nil || b.Altitude != nil {
		if a.Altitude == nil || b.Altitude == nil {
			return false
		}
	}
	if math.Abs(altA-altB) > 100 {
		return false
	}

	hdgA := positionDedupHeading(a)
	hdgB := positionDedupHeading(b)
	if hdgA < 0 && hdgB < 0 {
		// 两者都缺失 - 匹配
	} else if hdgA < 0 || hdgB < 0 {
		return false
	} else if math.Abs(math.Round(hdgA)-math.Round(hdgB)) > 1 {
		return false
	}

	if (a.SpeedTrue == nil) != (b.SpeedTrue == nil) {
		return false
	}
	if math.Abs(math.Round(adsb.NumberOrZero(a.SpeedTrue))-math.Round(adsb.NumberOrZero(b.SpeedTrue))) > 1 {
		return false
	}
	if (a.SpeedGS == nil) != (b.SpeedGS == nil) {
		return false
	}
	if math.Abs(math.Round(adsb.NumberOrZero(a.SpeedGS))-math.Round(adsb.NumberOrZero(b.SpeedGS))) > 1 {
		return false
	}

	return true
}

// deduplicateHistory 移除具有实际上相同显示值(容差为 1)的连续位置,
// 并为剩余位置添加跳过计数注释,以便 UI 显示分隔符。
// SkippedBefore:在上一个保留位置和此位置之间省略了 N 个重复位置。
// SkippedAfter:在最后一个保留位置之后省略了 N 个尾部重复位置。
// 对于小数据集(<60 个位置),返回所有位置而不分组。
func deduplicateHistory(positions []adsb.Position) []adsb.Position {
	if len(positions) < 60 {
		return positions
	}

	result := make([]adsb.Position, 0, len(positions))
	skippedCount := 0

	for i := 0; i < len(positions); i++ {
		if i > 0 && positionsMatchForDedup(positions[i], result[len(result)-1]) {
			skippedCount++
			continue
		}

		// 新的不同行 - 用之前跳过的数量进行注释
		pos := positions[i]
		if skippedCount > 0 {
			pos.SkippedBefore = skippedCount
			skippedCount = 0
		}

		result = append(result, pos)
	}

	// 尾部的重复项 - 注释最后一个保留位置
	if skippedCount > 0 && len(result) > 0 {
		result[len(result)-1].SkippedAfter = skippedCount
	}

	return result
}

func roundFloat(value float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(value*pow) / pow
}

func roundedFloatPtr(value *float64, decimals int) *float64 {
	if value == nil {
		return nil
	}
	rounded := roundFloat(*value, decimals)
	return &rounded
}

func deriveVerticalSpeedAt(positions []adsb.Position, index int) (*float64, bool) {
	if index < 0 || index >= len(positions) || positions[index].Altitude == nil {
		return nil, false
	}
	current := positions[index]
	neighborIndexes := []int{index - 1, index + 1}
	for _, neighborIndex := range neighborIndexes {
		if neighborIndex < 0 || neighborIndex >= len(positions) {
			continue
		}
		neighbor := positions[neighborIndex]
		if neighbor.Altitude == nil {
			continue
		}
		deltaMinutes := current.Timestamp.Sub(neighbor.Timestamp).Minutes()
		if math.Abs(deltaMinutes) < 1e-6 {
			continue
		}
		verticalSpeed := (*current.Altitude - *neighbor.Altitude) / deltaMinutes
		return &verticalSpeed, true
	}
	return nil, false
}

func normalizeTrackPositions(positions []adsb.Position) []adsb.Position {
	normalized := make([]adsb.Position, len(positions))
	copy(normalized, positions)

	for i := range normalized {
		if normalized[i].VerticalSpeed == nil {
			if derivedVerticalSpeed, ok := deriveVerticalSpeedAt(normalized, i); ok {
				normalized[i].VerticalSpeed = derivedVerticalSpeed
			}
		}

		normalized[i].Lat = roundedFloatPtr(normalized[i].Lat, 6)
		normalized[i].Lon = roundedFloatPtr(normalized[i].Lon, 6)
		normalized[i].Altitude = roundedFloatPtr(normalized[i].Altitude, 0)
		normalized[i].SpeedTrue = roundedFloatPtr(normalized[i].SpeedTrue, 0)
		normalized[i].SpeedGS = roundedFloatPtr(normalized[i].SpeedGS, 0)
		normalized[i].Track = roundedFloatPtr(normalized[i].Track, 0)
		normalized[i].TrueHeading = roundedFloatPtr(normalized[i].TrueHeading, 0)
		normalized[i].MagHeading = roundedFloatPtr(normalized[i].MagHeading, 0)
		normalized[i].VerticalSpeed = roundedFloatPtr(normalized[i].VerticalSpeed, 0)
	}

	return normalized
}

// GetAircraftTracks 返回飞行器的历史和未来航迹
func (h *Handler) GetAircraftTracks(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取 hex ID
	hex := chi.URLParam(r, "id")
	if hex == "" {
		http.Error(w, "Missing aircraft ID", http.StatusBadRequest)
		return
	}

	// 获取限制参数(默认为 1000)
	limitStr := r.URL.Query().Get("limit")
	limit := 1000
	if limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	// 获取基本信息的飞行器数据
	aircraft, found := h.adsbService.GetAircraftByHex(hex)
	if !found {
		http.Error(w, "Aircraft not found", http.StatusNotFound)
		return
	}

	// 获取带限制的位置历史
	history, err := h.adsbService.GetPositionHistoryWithLimit(hex, limit)
	if err != nil {
		h.logger.Error("获取位置历史失败",
			logger.Error(err),
			logger.String("hex", hex),
			logger.Int("limit", limit))
		http.Error(w, "Failed to get position history", http.StatusInternalServerError)
		return
	}

	// 计算每个历史位置的距离
	filteredHistory := make([]adsb.Position, 0, len(history))
	for i := range history {
		if history[i].Lat == nil || history[i].Lon == nil {
			continue
		}
		if *history[i].Lat == 0 && *history[i].Lon == 0 {
			continue
		}
		distMeters := haversine(*history[i].Lat, *history[i].Lon, h.config.Station.Latitude, h.config.Station.Longitude)
		distNM := math.Round(distMeters/1852.0*10) / 10 // 将米转换为海里并四舍五入到 1 位小数
		history[i].Distance = &distNM
		filteredHistory = append(filteredHistory, history[i])
	}
	history = filteredHistory

	// 去除具有相同显示值的连续位置重复项
	history = deduplicateHistory(history)
	history = normalizeTrackPositions(history)

	// 计算当前距站点的距离
	var distance *float64
	if aircraft.ADSB != nil && aircraft.ADSB.HasPosition() {
		lat, lon, _ := aircraft.ADSB.Position()
		distMeters := haversine(lat, lon, h.config.Station.Latitude, h.config.Station.Longitude)
		distNM := math.Round(distMeters/1852.0*10) / 10 // 将米转换为海里并四舍五入到 1 位小数
		distance = &distNM
	}

	// 计算每个未来位置的距离
	future := aircraft.Future
	for i := range future {
		if future[i].Lat != nil && future[i].Lon != nil {
			distMeters := haversine(*future[i].Lat, *future[i].Lon, h.config.Station.Latitude, h.config.Station.Longitude)
			distNM := math.Round(distMeters/1852.0*10) / 10 // 将米转换为海里并四舍五入到 1 位小数
			future[i].Distance = &distNM
		}
	}
	future = normalizeTrackPositions(future)

	hindcast := normalizeTrackPositions(aircraft.Hindcast)

	// 获取阶段历史
	phaseHistory, err := h.adsbService.GetPhaseHistory(hex)
	if err != nil {
		h.logger.Error("获取阶段历史失败",
			logger.Error(err),
			logger.String("hex", hex))
		phaseHistory = []adsb.PhaseChange{}
	}

	// 创建响应
	response := adsb.AircraftTracksResponse{
		Hex:          aircraft.Hex,
		Flight:       aircraft.Flight,
		Distance:     distance,
		History:      history,
		Future:       future,
		Hindcast:     hindcast,
		PhaseHistory: phaseHistory,
	}

	// 调试:打印历史中的一些 mag_heading 值
	h.logger.Debug("GetAircraftTracks 响应",
		logger.String("hex", hex),
		logger.Int("history_count", len(response.History)),
		logger.Int("future_count", len(response.Future)))

	if len(response.History) > 0 {
		for i, pos := range response.History[:min(3, len(response.History))] {
			h.logger.Debug("历史位置",
				logger.Int("index", i),
				logger.Float64("mag_heading", adsb.NumberOrZero(pos.MagHeading)),
				logger.Float64("true_heading", adsb.NumberOrZero(pos.TrueHeading)),
				logger.String("timestamp", pos.Timestamp.Format(time.RFC3339)))
		}
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// GetHealth 返回 API 的健康状态
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	lastFetch, status := h.adsbService.GetStatus()

	response := map[string]interface{}{
		"status":         status,
		"last_fetch":     lastFetch,
		"aircraft_count": len(h.adsbService.GetAllAircraft()),
	}

	WriteJSON(w, http.StatusOK, response)
}

// GetConfig 返回公开的配置
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	// 创建仅包含公开值的清理后的配置
	publicConfig := map[string]interface{}{
		"adsb": map[string]interface{}{
			"fetch_interval_seconds": h.config.ADSB.FetchIntervalSecs,
		},
		"storage": map[string]interface{}{
			"sqlite_base_path": h.config.Storage.SQLiteBasePath,
		},
		"frequencies": map[string]interface{}{
			"buffer_size_kb":          h.config.Frequencies.BufferSizeKB,
			"reconnect_interval_secs": h.config.Frequencies.ReconnectIntervalSecs,
		},
		"atc_chat": map[string]interface{}{
			"enabled": h.config.ATCChat.Enabled,
		},
	}

	WriteJSON(w, http.StatusOK, publicConfig)
}

// GetADSBSourceStatus 返回 ADS-B 源模式、健康状态以及可选的接收器/统计数据负载。
func (h *Handler) GetADSBSourceStatus(w http.ResponseWriter, r *http.Request) {
	status := h.adsbService.GetSourceStatus()
	WriteJSON(w, http.StatusOK, status)
}

// GetStationConfig 返回站点配置(纬度、经度、海拔高度)
func (h *Handler) GetStationConfig(w http.ResponseWriter, r *http.Request) {
	// 获取有效坐标(如果设置了覆盖,则使用覆盖值,否则使用配置值)
	effectiveLat, effectiveLon := h.adsbService.GetEffectiveStationCoords()

	stationCfg := struct {
		Latitude         float64            `json:"latitude"`
		Longitude        float64            `json:"longitude"`
		ElevationFeet    int                `json:"elevation_feet"`
		CruiseAltitudeFt int                `json:"cruise_altitude_ft"`
		AirportCode      string             `json:"airport_code"`
		Runways          interface{}        `json:"runways,omitempty"`
		RunwayInUse      []adsb.RunwayScore `json:"runway_in_use,omitempty"`
		FetchErrors      []string           `json:"fetch_errors,omitempty"`
		// 天气配置标志
		FetchMETAR  bool `json:"fetch_metar"`
		FetchTAF    bool `json:"fetch_taf"`
		FetchNOTAMs bool `json:"fetch_notams"`
		// 站点覆盖信息
		OverrideActive bool `json:"override_active"`
	}{
		Latitude:         effectiveLat,
		Longitude:        effectiveLon,
		ElevationFeet:    h.config.Station.ElevationFeet,
		CruiseAltitudeFt: h.config.FlightPhases.CruiseAltitudeFt,
		AirportCode:      h.config.Station.AirportCode,
		FetchMETAR:       h.config.Weather.FetchMETAR,
		FetchTAF:         h.config.Weather.FetchTAF,
		FetchNOTAMs:      h.config.Weather.FetchNOTAMs,
		OverrideActive:   effectiveLat != h.config.Station.Latitude || effectiveLon != h.config.Station.Longitude,
	}

	// 跟踪是否有任何数据获取失败
	var fetchErrors []string

	// 从参考服务构建跑道数据
	if h.refService != nil {
		runwayData := h.buildRunwayResponse()
		stationCfg.Runways = runwayData
	}

	// 添加使用中的跑道评分
	if scores := h.adsbService.GetRunwayInUseScores(3); len(scores) > 0 {
		stationCfg.RunwayInUse = scores
	}

	// 如果发生任何错误,将获取错误添加到响应
	if len(fetchErrors) > 0 {
		stationCfg.FetchErrors = fetchErrors
	}

	WriteJSON(w, http.StatusOK, stationCfg)
}

// SetStationOverride 设置或清除站点坐标覆盖
func (h *Handler) SetStationOverride(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Latitude  *float64 `json:"latitude"`  // nil 表示清除覆盖
		Longitude *float64 `json:"longitude"` // nil 表示清除覆盖
	}

	// 解析请求体
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("解析站点覆盖请求失败", logger.Error(err))
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 如果提供了坐标则进行验证
	if req.Latitude != nil && req.Longitude != nil {
		lat, lon := *req.Latitude, *req.Longitude

		// 基本坐标验证
		if lat < -90 || lat > 90 {
			http.Error(w, "Invalid latitude: must be between -90 and 90", http.StatusBadRequest)
			return
		}
		if lon < -180 || lon > 180 {
			http.Error(w, "Invalid longitude: must be between -180 and 180", http.StatusBadRequest)
			return
		}

		// 设置覆盖坐标
		h.adsbService.SetStationOverride(lat, lon)
		h.logger.Info("通过 API 设置站点覆盖坐标",
			logger.Float64("latitude", lat),
			logger.Float64("longitude", lon))

		response := struct {
			Success   bool    `json:"success"`
			Message   string  `json:"message"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		}{
			Success:   true,
			Message:   "Station override coordinates set successfully",
			Latitude:  lat,
			Longitude: lon,
		}
		WriteJSON(w, http.StatusOK, response)
	} else {
		// 清除覆盖坐标
		h.adsbService.ClearStationOverride()
		h.logger.Info("通过 API 清除站点覆盖坐标")

		response := struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}{
			Success: true,
			Message: "Station override coordinates cleared successfully",
		}
		WriteJSON(w, http.StatusOK, response)
	}
}

// GetWeatherData 返回缓存的天气数据(METAR、TAF、NOTAMs)
func (h *Handler) GetWeatherData(w http.ResponseWriter, r *http.Request) {
	if h.weatherService == nil {
		// 天气服务不可用
		weatherData := struct {
			METAR       interface{} `json:"metar,omitempty"`
			TAF         interface{} `json:"taf,omitempty"`
			NOTAMs      interface{} `json:"notams,omitempty"`
			LastUpdated string      `json:"last_updated"`
			FetchErrors []string    `json:"fetch_errors,omitempty"`
		}{
			LastUpdated: time.Now().Format(time.RFC3339),
			FetchErrors: []string{"Weather service not available"},
		}
		WriteJSON(w, http.StatusOK, weatherData)
		return
	}

	// 从服务获取天气数据
	weatherData := h.weatherService.GetWeatherData()
	WriteJSON(w, http.StatusOK, weatherData)
}

// buildRunwayResponse 从预计算的参考数据构建跑道 JSON 响应。
// 匹配前端 drawRunways() 期望的相同 JSON 形状。
func (h *Handler) buildRunwayResponse() interface{} {
	homeData := h.refService.GetHomeRunwayData()
	extensions := h.refService.GetHomeRunwayExtensions()

	type Point struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Distance  float64 `json:"distance,omitempty"`
	}

	// 将扩展转换为前端期望的 Point 类型
	extResponse := make(map[string]map[string][]Point)
	for pairKey, ends := range extensions {
		extResponse[pairKey] = make(map[string][]Point)
		for endID, pts := range ends {
			points := make([]Point, len(pts))
			for i, p := range pts {
				points[i] = Point{Latitude: p.Latitude, Longitude: p.Longitude, Distance: p.Distance}
			}
			extResponse[pairKey][endID] = points
		}
	}

	return struct {
		Airport          string `json:"airport"`
		RunwayThresholds map[string]map[string]struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"runway_thresholds"`
		RunwayExtensions map[string]map[string][]Point `json:"runway_extensions"`
	}{
		Airport:          homeData.Airport,
		RunwayThresholds: homeData.RunwayThresholds,
		RunwayExtensions: extResponse,
	}
}

// GetAirports 返回配置的显示范围内的所有机场
func (h *Handler) GetAirports(w http.ResponseWriter, r *http.Request) {
	if h.refService == nil {
		WriteJSON(w, http.StatusOK, []interface{}{})
		return
	}
	WriteJSON(w, http.StatusOK, h.refService.GetAirportsOnly())
}

// GetHeliports 返回配置的显示范围内的所有直升机机场
func (h *Handler) GetHeliports(w http.ResponseWriter, r *http.Request) {
	if h.refService == nil {
		WriteJSON(w, http.StatusOK, []interface{}{})
		return
	}
	WriteJSON(w, http.StatusOK, h.refService.GetHeliportsOnly())
}

// GetAirportByIdent 返回带完整详细信息(包括频率)的单个机场
func (h *Handler) GetAirportByIdent(w http.ResponseWriter, r *http.Request) {
	ident := strings.ToUpper(chi.URLParam(r, "ident"))
	if h.refService == nil {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "reference service not available"})
		return
	}
	airport := h.refService.GetAirport(ident)
	if airport == nil {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "airport not found"})
		return
	}

	// 还包括相关跑道
	var airportRunways []*reference.RunwayInfo
	for _, rwy := range h.refService.GetRunways() {
		if strings.EqualFold(rwy.AirportIdent, ident) {
			airportRunways = append(airportRunways, rwy)
		}
	}

	response := struct {
		*reference.AirportInfo
		Runways []*reference.RunwayInfo `json:"runways,omitempty"`
	}{
		AirportInfo: airport,
		Runways:     airportRunways,
	}
	WriteJSON(w, http.StatusOK, response)
}

// GetNavaids 返回配置的显示范围内的所有导航台
func (h *Handler) GetNavaids(w http.ResponseWriter, r *http.Request) {
	if h.refService == nil {
		WriteJSON(w, http.StatusOK, []interface{}{})
		return
	}
	WriteJSON(w, http.StatusOK, h.refService.GetNavaids())
}

// GetNavaidByIdent 返回所有与给定标识匹配的导航台
func (h *Handler) GetNavaidByIdent(w http.ResponseWriter, r *http.Request) {
	ident := strings.ToUpper(chi.URLParam(r, "ident"))
	if h.refService == nil {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "reference service not available"})
		return
	}
	navaids := h.refService.GetNavaidsByIdent(ident)
	if len(navaids) == 0 {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "navaid not found"})
		return
	}
	WriteJSON(w, http.StatusOK, navaids)
}

// GetRunways 返回配置的显示范围内的所有跑道
func (h *Handler) GetRunways(w http.ResponseWriter, r *http.Request) {
	if h.refService == nil {
		WriteJSON(w, http.StatusOK, []interface{}{})
		return
	}
	WriteJSON(w, http.StatusOK, h.refService.GetRunways())
}

// calculateBearing 计算从点 1 到点 2 的初始方位
func calculateBearing(lat1, lon1, lat2, lon2 float64) float64 {
	// 转换为弧度
	lat1 = lat1 * math.Pi / 180
	lon1 = lon1 * math.Pi / 180
	lat2 = lat2 * math.Pi / 180
	lon2 = lon2 * math.Pi / 180

	// 计算方位
	y := math.Sin(lon2-lon1) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(lon2-lon1)
	bearing := math.Atan2(y, x) * 180 / math.Pi

	// 归一化到 0-360
	return math.Mod(math.Mod(bearing, 360)+360, 360)
}

// calculateDestinationPoint 根据起始点、方位和距离计算目的点
func calculateDestinationPoint(lat, lon, bearing, distanceNM float64) (float64, float64) {
	// 转换为弧度
	lat = lat * math.Pi / 180
	lon = lon * math.Pi / 180
	bearing = bearing * math.Pi / 180

	// 地球半径(海里)
	earthRadius := 3440.065 // 6371 km / 1.852 km/nm

	// 计算目的点
	distRatio := distanceNM / earthRadius
	lat2 := math.Asin(math.Sin(lat)*math.Cos(distRatio) + math.Cos(lat)*math.Sin(distRatio)*math.Cos(bearing))
	lon2 := lon + math.Atan2(
		math.Sin(bearing)*math.Sin(distRatio)*math.Cos(lat),
		math.Cos(distRatio)-math.Sin(lat)*math.Sin(lat2),
	)

	// 转换回度数
	lat2 = lat2 * 180 / math.Pi
	lon2 = lon2 * 180 / math.Pi

	return lat2, lon2
}

// fetchMetarData 使用重试逻辑从 Windy API 获取 METAR 数据
func (h *Handler) fetchMetarData(airportCode string) (interface{}, error) {
	url := fmt.Sprintf("https://node.windy.com/airports/metar/%s", airportCode)

	// 创建一个新的带增加超时的 HTTP 客户端
	client := &http.Client{
		Timeout: 10 * time.Second, // 从 5 秒增加到 10 秒
	}

	// 重试配置
	maxRetries := 2
	var lastErr error
	var metarData interface{}

	// 尝试通过重试获取
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 重试之间的指数退避
			backoffDuration := time.Duration(500*(1<<uint(attempt-1))) * time.Millisecond
			h.logger.Info("正在重试 METAR 数据获取",
				logger.String("airport", airportCode),
				logger.Int("attempt", attempt),
				logger.String("backoff", backoffDuration.String()))
			time.Sleep(backoffDuration)
		}

		// 发起请求
		resp, err := client.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("向 Windy API 发起请求出错: %w", err)
			h.logger.Warn("METAR API 请求失败,可能会重试",
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 确保响应体被关闭
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("意外的状态码: %d", resp.StatusCode)
			h.logger.Warn("METAR API 返回非 OK 状态,可能会重试",
				logger.String("airport", airportCode),
				logger.Int("status_code", resp.StatusCode),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 读取和解析响应
		if err := json.NewDecoder(resp.Body).Decode(&metarData); err != nil {
			lastErr = fmt.Errorf("解码 METAR 数据出错: %w", err)
			h.logger.Warn("解码 METAR 数据失败,可能会重试",
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 成功 - 返回数据
		if attempt > 0 {
			h.logger.Info("重试后成功获取 METAR 数据",
				logger.String("airport", airportCode),
				logger.Int("attempts_needed", attempt+1))
		}
		return metarData, nil
	}

	// 如果到达这里,所有尝试都失败了
	h.logger.Error("所有获取 METAR 数据的尝试均失败",
		logger.String("airport", airportCode),
		logger.Error(lastErr),
		logger.Int("max_attempts", maxRetries+1))
	return nil, lastErr
}

// fetchTAFData 使用重试逻辑从 Windy API 获取 TAF 数据
func (h *Handler) fetchTAFData(airportCode string) (interface{}, error) {
	url := fmt.Sprintf("https://node.windy.com/airports/taf/%s", airportCode)

	// 创建一个新的带增加超时的 HTTP 客户端
	client := &http.Client{
		Timeout: 10 * time.Second, // 从 5 秒增加到 10 秒
	}

	// 重试配置
	maxRetries := 2
	var lastErr error
	var tafData interface{}

	// 尝试通过重试获取
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 重试之间的指数退避
			backoffDuration := time.Duration(500*(1<<uint(attempt-1))) * time.Millisecond
			h.logger.Info("正在重试 TAF 数据获取",
				logger.String("airport", airportCode),
				logger.Int("attempt", attempt),
				logger.String("backoff", backoffDuration.String()))
			time.Sleep(backoffDuration)
		}

		// 发起请求
		resp, err := client.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("向 Windy API 发起请求出错: %w", err)
			h.logger.Warn("TAF API 请求失败,可能会重试",
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 确保响应体被关闭
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("意外的状态码: %d", resp.StatusCode)
			h.logger.Warn("TAF API 返回非 OK 状态,可能会重试",
				logger.String("airport", airportCode),
				logger.Int("status_code", resp.StatusCode),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 读取和解析响应
		if err := json.NewDecoder(resp.Body).Decode(&tafData); err != nil {
			lastErr = fmt.Errorf("解码 TAF 数据出错: %w", err)
			h.logger.Warn("解码 TAF 数据失败,可能会重试",
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 成功 - 返回数据
		if attempt > 0 {
			h.logger.Info("重试后成功获取 TAF 数据",
				logger.String("airport", airportCode),
				logger.Int("attempts_needed", attempt+1))
		}
		return tafData, nil
	}

	// 如果到达这里,所有尝试都失败了
	h.logger.Error("所有获取 TAF 数据的尝试均失败",
		logger.String("airport", airportCode),
		logger.Error(lastErr),
		logger.Int("max_attempts", maxRetries+1))
	return nil, lastErr
}

// fetchNOTAMData 使用重试逻辑从 Windy API 获取 NOTAM 数据
func (h *Handler) fetchNOTAMData(airportCode string) (interface{}, error) {
	url := fmt.Sprintf("https://node.windy.com/airports/notams/%s", airportCode)

	// 创建一个新的带增加超时的 HTTP 客户端
	client := &http.Client{
		Timeout: 10 * time.Second, // 从 5 秒增加到 10 秒
	}

	// 重试配置
	maxRetries := 2
	var lastErr error
	var notamData interface{}

	// 尝试通过重试获取
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 重试之间的指数退避
			backoffDuration := time.Duration(500*(1<<uint(attempt-1))) * time.Millisecond
			h.logger.Info("正在重试 NOTAM 数据获取",
				logger.String("airport", airportCode),
				logger.Int("attempt", attempt),
				logger.String("backoff", backoffDuration.String()))
			time.Sleep(backoffDuration)
		}

		// 发起请求
		resp, err := client.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("向 Windy API 发起请求出错: %w", err)
			h.logger.Warn("NOTAM API 请求失败,可能会重试",
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 确保响应体被关闭
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("意外的状态码: %d", resp.StatusCode)
			h.logger.Warn("NOTAM API 返回非 OK 状态,可能会重试",
				logger.String("airport", airportCode),
				logger.Int("status_code", resp.StatusCode),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 读取和解析响应
		if err := json.NewDecoder(resp.Body).Decode(&notamData); err != nil {
			lastErr = fmt.Errorf("解码 NOTAM 数据出错: %w", err)
			h.logger.Warn("解码 NOTAM 数据失败,可能会重试",
				logger.String("airport", airportCode),
				logger.Error(err),
				logger.Int("attempt", attempt+1),
				logger.Int("max_attempts", maxRetries+1))
			continue
		}

		// 成功 - 返回数据
		if attempt > 0 {
			h.logger.Info("重试后成功获取 NOTAM 数据",
				logger.String("airport", airportCode),
				logger.Int("attempts_needed", attempt+1))
		}
		return notamData, nil
	}

	// 如果到达这里,所有尝试都失败了
	h.logger.Error("所有获取 NOTAM 数据的尝试均失败",
		logger.String("airport", airportCode),
		logger.Error(lastErr),
		logger.Int("max_attempts", maxRetries+1))
	return nil, lastErr
}

// GetAllFrequencies 返回所有带有最近转写的频率
func (h *Handler) GetAllFrequencies(w http.ResponseWriter, r *http.Request) {
	// 获取所有频率
	frequencies := h.frequenciesService.GetAllFrequencies()

	// 每个频率获取最近 100 条转写
	transcriptionsByFreq := make(map[string]interface{})
	if h.transcriptionStorage != nil {
		for _, freq := range frequencies {
			txns, err := h.transcriptionStorage.GetTranscriptionsByFrequency(freq.ID, 100, 0)
			if err != nil {
				h.logger.Error("获取频率的转写失败",
					logger.String("frequency_id", freq.ID),
					logger.Error(err))
				continue
			}
			if len(txns) > 0 {
				transcriptionsByFreq[freq.ID] = txns
			}
		}
	}

	// 创建响应
	response := map[string]interface{}{
		"timestamp":      time.Now().UTC(),
		"count":          len(frequencies),
		"frequencies":    frequencies,
		"transcriptions": transcriptionsByFreq,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// GetFrequencyByID 通过 ID 返回频率
func (h *Handler) GetFrequencyByID(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取频率 ID
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Missing frequency ID", http.StatusBadRequest)
		return
	}

	// 获取频率数据
	frequency, found := h.frequenciesService.GetFrequencyByID(id)
	if !found {
		http.Error(w, "Frequency not found", http.StatusNotFound)
		return
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, frequency)
}

// StreamAudio 流式传输频率的音频
func (h *Handler) StreamAudio(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取频率 ID
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Missing frequency ID", http.StatusBadRequest)
		return
	}

	// 从查询参数获取客户端 ID
	clientID := r.URL.Query().Get("id")
	if clientID == "" {
		// 如果未提供则生成一个随机客户端 ID
		clientID = fmt.Sprintf("client-%d", time.Now().UnixNano())
	}

	clientRemoteAddr := r.RemoteAddr

	// 设置二进制流头部
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Connection", "keep-alive")                // 添加 keep-alive
	w.Header().Set("Keep-Alive", "timeout=86400, max=604800") // 添加 keep-alive 超时

	// 对于 HEAD 请求,只返回头部
	if r.Method == "HEAD" {
		return
	}

	// 直接使用请求的上下文 - 当客户端断开连接时它将被取消
	ctx := r.Context()

	h.logger.Debug("客户端请求音频流",
		logger.String("id", id),
		logger.String("client_id", clientID),
		logger.String("remote_addr", clientRemoteAddr))

	// 使用客户端 ID 获取音频流
	stream, contentType, err := h.frequenciesService.GetAudioStream(ctx, id, clientID)
	if err != nil {
		// 检查错误是否由于客户端已经连接
		if strings.Contains(err.Error(), "client already connected") {
			h.logger.Debug("客户端已连接,返回成功",
				logger.String("id", id),
				logger.String("client_id", clientID),
				logger.String("remote_addr", clientRemoteAddr))

			// 返回最小响应表明客户端已连接
			w.Header().Set("X-Already-Connected", "true")
			w.WriteHeader(http.StatusOK)
			return
		}

		h.logger.Error("获取音频流失败",
			logger.String("id", id),
			logger.String("client_id", clientID),
			logger.String("remote_addr", clientRemoteAddr),
			logger.Error(err),
		)
		http.Error(w, "Stream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer stream.Close() // 关键:确保调用 ClientStreamReader.Close()

	h.logger.Debug("客户端已连接到音频流",
		logger.String("id", id),
		logger.String("client_id", clientID),
		logger.String("remote_addr", clientRemoteAddr),
		logger.String("content_type", contentType),
	)

	// 连接监控设置
	connectionStartTime := time.Now()

	// 使用缓冲区以提高性能
	buf := make([]byte, 4096)

	// 跟踪连续错误以检测客户端断开连接
	consecutiveErrors := 0
	bytesWritten := 0

	// 向客户端流式传输数据
	lastProgressLog := time.Now()
	for {
		// 检查客户端是否已断开连接
		select {
		case <-ctx.Done():
			h.logger.Info("客户端上下文已结束,停止流",
				logger.String("id", id),
				logger.String("client_id", clientID),
				logger.String("remote_addr", clientRemoteAddr),
				logger.String("reason", ctx.Err().Error()),
				logger.Int("total_bytes_written", bytesWritten),
				logger.String("connection_duration", time.Since(connectionStartTime).String()),
			)
			return
		default:
			// 继续流式传输
		}

		// 从流读取 - 如果没有数据,这将在 5 秒后超时
		n, err := stream.Read(buf)

		if err != nil {
			if err == io.EOF {
				h.logger.Warn("流意外地到达 EOF",
					logger.String("id", id),
					logger.String("client_id", clientID),
					logger.Int("bytes_written_before_eof", bytesWritten),
					logger.String("connection_duration", time.Since(connectionStartTime).String()))
				return
			}

			h.logger.Warn("从流读取出错",
				logger.String("id", id),
				logger.String("client_id", clientID),
				logger.String("error_type", fmt.Sprintf("%T", err)),
				logger.Error(err),
				logger.Int("consecutive_errors", consecutiveErrors+1))

			consecutiveErrors++
			if consecutiveErrors > 3 {
				h.logger.Error("连续读取错误过多,关闭流",
					logger.String("id", id),
					logger.String("client_id", clientID),
					logger.Int("total_consecutive_errors", consecutiveErrors),
					logger.Int("bytes_written_before_failure", bytesWritten),
					logger.String("connection_duration", time.Since(connectionStartTime).String()))
				return
			}

			// 重试前稍作暂停
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// 成功读取后重置错误计数器
		consecutiveErrors = 0

		// 如果获取到数据,写入到客户端
		if n > 0 {
			_, err = w.Write(buf[:n])
			if err != nil {
				h.logger.Warn("向客户端写入出错,关闭流",
					logger.String("id", id),
					logger.String("client_id", clientID),
					logger.String("error_type", fmt.Sprintf("%T", err)),
					logger.Error(err),
					logger.Int("bytes_written_before_error", bytesWritten),
					logger.String("connection_duration", time.Since(connectionStartTime).String()))
				return
			}

			bytesWritten += n

			// 立即刷新数据
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			// 每 100KB 数据或每 60 秒记录一次,以先到者为准
			if bytesWritten%102400 < n || time.Since(lastProgressLog) > 60*time.Second {
				h.logger.Debug("流传输进度",
					logger.String("id", id),
					logger.String("client_id", clientID),
					logger.Int("bytes_written", bytesWritten),
					logger.String("connection_duration", time.Since(connectionStartTime).String()))
				lastProgressLog = time.Now()
			}
		}
	}
}

// parseAircraftFilters 从请求解析飞行器过滤参数
func parseAircraftFilters(r *http.Request) (float64, float64, string, []string, int, *time.Time, *time.Time, *time.Time, *time.Time, float64, float64, float64, string, string, bool, bool) {
	minAltitude := 0.0
	maxAltitude := 60000.0
	callsign := ""
	var status []string
	lastSeenMinutes := 0 // 默认为 0(不过滤)

	// 新过滤参数
	var tookOffAfter, tookOffBefore, landedAfter, landedBefore *time.Time
	distanceNM := 0.0
	refLat, refLon := 0.0, 0.0
	refHex := ""
	refFlight := ""
	simple := false // 简单模式返回轻量级响应

	// 解析现有过滤器
	if minStr := r.URL.Query().Get("min_altitude"); minStr != "" {
		if min, err := strconv.ParseFloat(minStr, 64); err == nil {
			minAltitude = min
		}
	}

	if maxStr := r.URL.Query().Get("max_altitude"); maxStr != "" {
		if max, err := strconv.ParseFloat(maxStr, 64); err == nil {
			maxAltitude = max
		}
	}

	callsign = r.URL.Query().Get("callsign")

	// 解析 status 过滤参数
	if statusStr := r.URL.Query().Get("status"); statusStr != "" {
		status = strings.Split(statusStr, ",")
		for i, s := range status {
			status[i] = strings.TrimSpace(s)
		}
	}

	// 解析 last_seen_minutes 过滤参数
	if lastSeenStr := r.URL.Query().Get("last_seen_minutes"); lastSeenStr != "" {
		if lastSeen, err := strconv.Atoi(lastSeenStr); err == nil && lastSeen > 0 {
			lastSeenMinutes = lastSeen
		}
	}

	// 解析新增的起飞时间过滤参数
	if tookOffAfterStr := r.URL.Query().Get("took_off_after"); tookOffAfterStr != "" {
		if t, err := time.Parse(time.RFC3339, tookOffAfterStr); err == nil {
			tookOffAfter = &t
		}
	}

	if tookOffBeforeStr := r.URL.Query().Get("took_off_before"); tookOffBeforeStr != "" {
		if t, err := time.Parse(time.RFC3339, tookOffBeforeStr); err == nil {
			tookOffBefore = &t
		}
	}

	// 解析新增的着陆时间过滤参数
	if landedAfterStr := r.URL.Query().Get("landed_after"); landedAfterStr != "" {
		if t, err := time.Parse(time.RFC3339, landedAfterStr); err == nil {
			landedAfter = &t
		}
	}

	if landedBeforeStr := r.URL.Query().Get("landed_before"); landedBeforeStr != "" {
		if t, err := time.Parse(time.RFC3339, landedBeforeStr); err == nil {
			landedBefore = &t
		}
	}

	// 解析距离过滤参数
	if distanceStr := r.URL.Query().Get("distance_nm"); distanceStr != "" {
		if dist, err := strconv.ParseFloat(distanceStr, 64); err == nil && dist > 0 {
			distanceNM = dist
		}
	}

	// 解析参考坐标参数
	if latStr := r.URL.Query().Get("ref_lat"); latStr != "" {
		if lat, err := strconv.ParseFloat(latStr, 64); err == nil {
			refLat = lat
		}
	}

	if lonStr := r.URL.Query().Get("ref_lon"); lonStr != "" {
		if lon, err := strconv.ParseFloat(lonStr, 64); err == nil {
			refLon = lon
		}
	}

	// 解析参考 hex 参数
	refHex = r.URL.Query().Get("ref_hex")

	// 解析参考航班号参数
	refFlight = r.URL.Query().Get("ref_flight")

	// 解析 exclude_other_airports_grounded 参数
	excludeOtherAirportsGrounded := false
	if excludeStr := r.URL.Query().Get("exclude_other_airports_grounded"); excludeStr != "" {
		if exclude, err := strconv.ParseBool(excludeStr); err == nil {
			excludeOtherAirportsGrounded = exclude
		} else if excludeStr == "1" {
			excludeOtherAirportsGrounded = true
		}
	}

	// 解析 simple 参数,用于返回轻量级响应
	if simpleStr := r.URL.Query().Get("simple"); simpleStr != "" {
		if s, err := strconv.ParseBool(simpleStr); err == nil {
			simple = s
		} else if simpleStr == "1" {
			simple = true
		}
	}

	return minAltitude, maxAltitude, callsign, status, lastSeenMinutes,
		tookOffAfter, tookOffBefore, landedAfter, landedBefore, distanceNM,
		refLat, refLon, refHex, refFlight, excludeOtherAirportsGrounded, simple
}

// getHexCoordinates 从飞行器 hex 码获取坐标
func (h *Handler) getHexCoordinates(hexCode string) (float64, float64, error) {
	// 通过 hex 码查找飞行器
	aircraft, found := h.adsbService.GetAircraftByHex(hexCode)
	if !found {
		return 0, 0, fmt.Errorf("未找到 hex 为 %s 的飞行器", hexCode)
	}

	if aircraft.ADSB == nil || !aircraft.ADSB.HasPosition() {
		return 0, 0, fmt.Errorf("hex 为 %s 的飞行器没有位置数据", hexCode)
	}
	lat, lon, _ := aircraft.ADSB.Position()
	return lat, lon, nil
}

// getRefAircraft 通过 hex 码获取参考飞行器
func (h *Handler) getRefAircraft(hexCode string) (*adsb.Aircraft, error) {
	// 通过 hex 码查找飞行器
	aircraft, found := h.adsbService.GetAircraftByHex(hexCode)
	if !found {
		return nil, fmt.Errorf("未找到 hex 为 %s 的飞行器", hexCode)
	}

	if aircraft.ADSB == nil || !aircraft.ADSB.HasPosition() {
		return nil, fmt.Errorf("hex 为 %s 的飞行器没有位置数据", hexCode)
	}

	return aircraft, nil
}

// getFlightCoordinates 从航班号或尾号获取坐标
func (h *Handler) getFlightCoordinates(flight string) (float64, float64, error) {
	// 通过航班号查找飞行器
	// 首先,获取所有飞行器
	allAircraft := h.adsbService.GetAllAircraft()

	// 找出与航班号匹配的飞行器
	for _, a := range allAircraft {
		if strings.EqualFold(strings.TrimSpace(a.Flight), strings.TrimSpace(flight)) {
			if a.ADSB == nil {
				return 0, 0, fmt.Errorf("航班 %s 的飞行器没有 ADSB 数据", flight)
			}

			if !a.ADSB.HasPosition() {
				return 0, 0, fmt.Errorf("航班 %s 的飞行器没有位置数据", flight)
			}
			lat, lon, _ := a.ADSB.Position()
			return lat, lon, nil
		}
	}
	return 0, 0, fmt.Errorf("未找到航班 %s 的飞行器", flight)
}

// WriteJSON 写入 JSON 响应
func WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// updateZeroValuesFromHistory 用位置历史中最后一个非零值更新飞行器的零值
func updateZeroValuesFromHistory(aircraft *adsb.Aircraft) {
	// 此函数不再需要,因为我们直接使用 ADSB 数据
	// 我们将其保留为空操作以保持向后兼容性
	_ = aircraft
}

// haversine 是 adsb.Haversine 的包装,用于向后兼容
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	return adsb.Haversine(lat1, lon1, lat2, lon2)
}

// 此函数已被 getHexCoordinates 和 getFlightCoordinates 替代

// CreateATCChatSession 创建一个新的 ATC 聊天会话
func (h *Handler) CreateATCChatSession(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	session, err := h.atcChatService.CreateSession(r.Context())
	if err != nil {
		// 检查是否为缺少 API 密钥的错误 - 优雅处理
		if strings.Contains(err.Error(), "OpenAI API key is required") {
			h.logger.Warn("ATC 聊天会话创建失败 - API 密钥未配置")
			http.Error(w, "ATC Chat requires OpenAI API key configuration", http.StatusServiceUnavailable)
			return
		}

		// 对于其他错误,以错误级别带堆栈跟踪记录
		h.logger.Error("创建 ATC 聊天会话失败", logger.Error(err))
		http.Error(w, fmt.Sprintf("Failed to create session: %v", err), http.StatusInternalServerError)
		return
	}

	h.logger.Info("已创建 ATC 聊天会话",
		logger.String("session_id", session.ID))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(session); err != nil {
		h.logger.Error("编码会话响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// EndATCChatSession 终止 ATC 聊天会话
func (h *Handler) EndATCChatSession(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	if err := h.atcChatService.EndSession(r.Context(), sessionID); err != nil {
		h.logger.Error("结束 ATC 聊天会话失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, fmt.Sprintf("Failed to end session: %v", err), http.StatusInternalServerError)
		return
	}

	h.logger.Info("已结束 ATC 聊天会话",
		logger.String("session_id", sessionID))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":     "success",
		"session_id": sessionID,
		"message":    "Session ended successfully",
	})
}

// HandleATCChatWebSocket 处理 ATC 聊天的 WebSocket 连接
func (h *Handler) HandleATCChatWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	// 创建 ATC 聊天处理器并委托给它们
	atcChatHandlers := NewATCChatHandlers(h.atcChatService, h.logger)

	// 更新 URL 参数以匹配 ATC 聊天处理器期望的内容
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionID", sessionID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	atcChatHandlers.WebSocketHandler(w, r)
}

// GetATCChatSessionStatus 返回 ATC 聊天会话的状态
func (h *Handler) GetATCChatSessionStatus(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	status, err := h.atcChatService.GetSessionStatus(sessionID)
	if err != nil {
		h.logger.Error("获取会话状态失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, fmt.Sprintf("Failed to get session status: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		h.logger.Error("编码状态响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// GetATCChatSessions 返回所有活跃的 ATC 聊天会话
func (h *Handler) GetATCChatSessions(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	sessions := h.atcChatService.ListActiveSessions()

	response := map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
		"status":   "success",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("编码会话响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// GetATCChatAirspaceStatus 返回 ATC 聊天的当前空域状态
func (h *Handler) GetATCChatAirspaceStatus(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	status := h.atcChatService.GetAirspaceStatus()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		h.logger.Error("编码空域状态响应失败", logger.Error(err))
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// UpdateATCChatSessionContext 使用最新的空域数据更新会话上下文
func (h *Handler) UpdateATCChatSessionContext(w http.ResponseWriter, r *http.Request) {
	if h.atcChatService == nil {
		http.Error(w, "ATC Chat service not available", http.StatusServiceUnavailable)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	h.logger.Debug("收到更新会话上下文的请求",
		logger.String("session_id", sessionID))

	// 生成将发送到 AI 的系统提示词和变量
	promptWithVars, err := h.atcChatService.GenerateSystemPromptWithVariables(sessionID)
	if err != nil {
		h.logger.Error("为上下文更新生成系统提示词失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, fmt.Sprintf("Failed to generate system prompt: %v", err), http.StatusInternalServerError)
		return
	}

	// 使用最新的空域数据更新会话上下文
	if err := h.atcChatService.UpdateSessionContextOnDemand(sessionID); err != nil {
		h.logger.Error("更新会话上下文失败",
			logger.String("session_id", sessionID),
			logger.Error(err))
		http.Error(w, fmt.Sprintf("Failed to update session context: %v", err), http.StatusInternalServerError)
		return
	}

	h.logger.Info("会话上下文更新成功",
		logger.String("session_id", sessionID))

	// 返回带有发送给 AI 的实际指令和单个变量的成功响应
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "success",
		"message":       "Session context updated with fresh airspace data",
		"instructions":  promptWithVars.Prompt,
		"prompt_length": len(promptWithVars.Prompt),
		"variables":     promptWithVars.Variables,
	})
}

// convertClearancesToAPIFormat 将放行许可记录转换为 API 格式
func (h *Handler) convertClearancesToAPIFormat(clearances []*sqlite.ClearanceRecord) []adsb.ClearanceData {
	result := make([]adsb.ClearanceData, len(clearances))
	now := time.Now().UTC()

	for i, c := range clearances {
		result[i] = adsb.ClearanceData{
			ID:              c.ID,
			Type:            c.ClearanceType,
			Text:            c.ClearanceText,
			Runway:          c.Runway,
			Timestamp:       c.Timestamp,
			Status:          c.Status,
			TimeSinceIssued: h.formatTimeSince(now.Sub(c.Timestamp)),
		}
	}

	return result
}

// formatTimeSince 将时长格式化为人类可读的字符串
func (h *Handler) formatTimeSince(duration time.Duration) string {
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	} else if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	} else if duration < 24*time.Hour {
		return fmt.Sprintf("%dh", int(duration.Hours()))
	} else {
		return fmt.Sprintf("%dd", int(duration.Hours()/24))
	}
}

// CreateSimulatedAircraft 创建一个新的模拟飞行器
func (h *Handler) CreateSimulatedAircraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lat          float64 `json:"lat"`
		Lon          float64 `json:"lon"`
		Altitude     float64 `json:"altitude"`
		Heading      float64 `json:"heading"`
		Speed        float64 `json:"speed"`
		VerticalRate float64 `json:"vertical_rate"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 验证输入
	if req.Lat < -90 || req.Lat > 90 || req.Lon < -180 || req.Lon > 180 {
		http.Error(w, "Invalid coordinates", http.StatusBadRequest)
		return
	}

	if req.Altitude < 0 || req.Altitude > 60000 {
		http.Error(w, "Invalid altitude (0-60000 ft)", http.StatusBadRequest)
		return
	}

	if req.Heading < 0 || req.Heading >= 360 {
		http.Error(w, "Invalid heading (0-359 degrees)", http.StatusBadRequest)
		return
	}

	if req.Speed < 0 || req.Speed > 500 {
		http.Error(w, "Invalid speed (0-500 knots)", http.StatusBadRequest)
		return
	}

	if req.VerticalRate < -3000 || req.VerticalRate > 3000 {
		http.Error(w, "Invalid vertical rate (-3000 to +3000 fpm)", http.StatusBadRequest)
		return
	}

	aircraft, err := h.simulationService.CreateAircraft(
		req.Lat, req.Lon, req.Altitude,
		req.Heading, req.Speed, req.VerticalRate,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.logger.Info("通过 API 创建了模拟飞行器",
		logger.String("hex", aircraft.Hex),
		logger.String("flight", aircraft.Flight))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "success",
		"aircraft": aircraft,
	})
}

// UpdateSimulationControls 更新模拟飞行器的控制参数
func (h *Handler) UpdateSimulationControls(w http.ResponseWriter, r *http.Request) {
	hex := chi.URLParam(r, "hex")
	if hex == "" {
		http.Error(w, "Missing hex parameter", http.StatusBadRequest)
		return
	}

	var req struct {
		Heading      float64 `json:"heading"`
		Speed        float64 `json:"speed"`
		VerticalRate float64 `json:"vertical_rate"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// 验证输入
	if req.Heading < 0 || req.Heading >= 360 {
		http.Error(w, "Invalid heading (0-359 degrees)", http.StatusBadRequest)
		return
	}

	if req.Speed < 0 || req.Speed > 500 {
		http.Error(w, "Invalid speed (0-500 knots)", http.StatusBadRequest)
		return
	}

	if req.VerticalRate < -3000 || req.VerticalRate > 3000 {
		http.Error(w, "Invalid vertical rate (-3000 to +3000 fpm)", http.StatusBadRequest)
		return
	}

	err := h.simulationService.UpdateControls(hex, req.Heading, req.Speed, req.VerticalRate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	h.logger.Debug("通过 API 更新了模拟控制",
		logger.String("hex", hex),
		logger.Float64("heading", req.Heading),
		logger.Float64("speed", req.Speed),
		logger.Float64("vertical_rate", req.VerticalRate))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
	})
}

// RemoveSimulatedAircraft 移除模拟飞行器
func (h *Handler) RemoveSimulatedAircraft(w http.ResponseWriter, r *http.Request) {
	hex := chi.URLParam(r, "hex")
	if hex == "" {
		http.Error(w, "Missing hex parameter", http.StatusBadRequest)
		return
	}

	err := h.simulationService.RemoveAircraft(hex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	h.logger.Info("通过 API 移除了模拟飞行器",
		logger.String("hex", hex))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
	})
}

// GetSimulatedAircraft 返回所有模拟飞行器
func (h *Handler) GetSimulatedAircraft(w http.ResponseWriter, r *http.Request) {
	aircraft := h.simulationService.GetAllAircraft()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aircraft)
}
