package templating

import (
	"fmt"
	"strings"
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/weather"
)

func firstNonEmptyString(values ...interface{}) string {
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(s)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// FormatAircraftData 格式化飞行器数据用于模板渲染
// 使用与 ATC 聊天相同的格式以保持一致性
func FormatAircraftData(aircraft []*adsb.Aircraft, airport AirportInfo) string {
	if len(aircraft) == 0 {
		return "No aircraft currently in the airspace."
	}

	// 按地面状态分离飞行器
	var airborne []*adsb.Aircraft
	var onGround []*adsb.Aircraft

	for _, ac := range aircraft {
		if ac.OnGround {
			onGround = append(onGround, ac)
		} else {
			airborne = append(airborne, ac)
		}
	}

	var builder strings.Builder

	// 空中飞行器部分
	builder.WriteString(fmt.Sprintf("AIRBORNE (%d aircraft):\n", len(airborne)))
	if len(airborne) == 0 {
		builder.WriteString("No airborne aircraft\n")
	} else {
		for _, ac := range airborne {
			builder.WriteString(formatAirborneAircraft(ac, airport))
			builder.WriteString("\n")
		}
	}

	builder.WriteString("\n")

	// 地面飞行器部分
	builder.WriteString(fmt.Sprintf("ON GROUND (%d aircraft):\n", len(onGround)))
	if len(onGround) == 0 {
		builder.WriteString("No ground aircraft\n")
	} else {
		for _, ac := range onGround {
			builder.WriteString(formatGroundAircraft(ac, airport))
			builder.WriteString("\n")
		}
	}

	return builder.String()
}

// formatAirborneAircraft 格式化单个空中飞行器以供显示
func formatAirborneAircraft(ac *adsb.Aircraft, airport AirportInfo) string {
	var builder strings.Builder

	// 基本信息 - 呼号和运营人
	callsign := ac.Flight
	if callsign == "" {
		callsign = "Unknown"
	}
	airline := ac.Airline
	if airline == "" {
		airline = "N/A"
	}
	operator := "N/A"
	if ac.BSDB != nil && strings.TrimSpace(ac.BSDB.RegisteredOwners) != "" {
		operator = strings.TrimSpace(ac.BSDB.RegisteredOwners)
	}
	typeCode := "N/A"
	if ac.BSDB != nil {
		if strings.TrimSpace(ac.BSDB.Type) != "" {
			typeCode = strings.TrimSpace(ac.BSDB.Type)
		} else if strings.TrimSpace(ac.BSDB.ICAOTypeCode) != "" {
			typeCode = strings.TrimSpace(ac.BSDB.ICAOTypeCode)
		}
	}
	if typeCode == "N/A" && ac.ADSB != nil && strings.TrimSpace(ac.ADSB.AircraftType) != "" {
		typeCode = strings.TrimSpace(ac.ADSB.AircraftType)
	}

	builder.WriteString(fmt.Sprintf("%s", callsign))
	builder.WriteString(fmt.Sprintf(" | Airline: %s | Operator: %s | Type: %s | ", airline, operator, typeCode))

	// 尾流类别
	if ac.ADSB != nil && ac.ADSB.Category != "" {
		builder.WriteString(fmt.Sprintf("Wake Category: %s | ", ac.ADSB.Category))
	}

	// 飞行参数
	if ac.ADSB != nil {
		builder.WriteString("Flight params: ")

		// 如果可用,使用磁航向(0-360° 都有效),否则回退到航迹
		if ac.ADSB.MagHeading != nil && *ac.ADSB.MagHeading >= 0 && *ac.ADSB.MagHeading <= 360 {
			builder.WriteString(fmt.Sprintf("HDG: %.0f", *ac.ADSB.MagHeading))
		} else if ac.ADSB.Track != nil && *ac.ADSB.Track != 0 {
			builder.WriteString(fmt.Sprintf("HDG: %.0f", *ac.ADSB.Track))
		}

		if ac.ADSB.TAS != nil {
			builder.WriteString(fmt.Sprintf(", TAS: %.0f kts", *ac.ADSB.TAS))
		}

		if ac.ADSB.GS != nil {
			builder.WriteString(fmt.Sprintf(", GS: %.0f kts", *ac.ADSB.GS))
		}

		if ac.ADSB.AltBaro.Float64() != 0 {
			builder.WriteString(fmt.Sprintf(", alt: %.0f ft", ac.ADSB.AltBaro.Float64()))
		}

		if ac.ADSB.BaroRate != nil {
			builder.WriteString(fmt.Sprintf(", VS: %.0f fpm", *ac.ADSB.BaroRate))
		}

		// 如果可用,添加应答机编码
		if ac.ADSB.Squawk != "" {
			builder.WriteString(fmt.Sprintf(", squawk: %s", ac.ADSB.Squawk))
		}

		builder.WriteString(", status: airborne")

		// 添加起飞时间 - 始终显示,如果未知则使用 N/A
		if ac.DateTookoff != nil {
			timeSince := time.Since(*ac.DateTookoff)
			builder.WriteString(fmt.Sprintf(", T/O: %s ago", formatDuration(timeSince)))
		} else {
			builder.WriteString(", T/O: N/A")
		}
	}

	// 机场位置(到机场的距离和方位)
	if ac.Distance != nil && ac.ADSB != nil && ac.ADSB.HasPosition() {
		lat, lon, _ := ac.ADSB.Position()
		bearingToStation := adsb.CalculateBearing(lat, lon, airport.Coordinates[0], airport.Coordinates[1])
		builder.WriteString(fmt.Sprintf(" | Airport position: %.1f NM, fly heading %.0f", *ac.Distance, bearingToStation))
	}

	if ac.ADSB != nil && ac.ADSB.ATCDerived != nil && ac.ADSB.ATCDerived.ETAStationSec != nil {
		builder.WriteString(fmt.Sprintf(" | ATC Derived: ETA to Station: %s", formatEtaToStation(*ac.ADSB.ATCDerived.ETAStationSec)))
	}

	// 飞行阶段
	if ac.Phase != nil && len(ac.Phase.Current) > 0 {
		currentPhase := ac.Phase.Current[0]
		fullPhaseName := getFullPhaseName(currentPhase.Phase)
		timeSince := time.Since(currentPhase.Timestamp)
		builder.WriteString(fmt.Sprintf(" | Phase: %s (%s)", fullPhaseName, formatDuration(timeSince)))
	}

	// 遥测状态
	builder.WriteString(fmt.Sprintf(" | Telemetry: %s", ac.Status))
	if !ac.LastSeen.IsZero() {
		timeSince := time.Since(ac.LastSeen)
		builder.WriteString(fmt.Sprintf(", Last seen: %s", formatDuration(timeSince)))
	}

	return builder.String()
}

// formatGroundAircraft 格式化单个地面飞行器以供显示
func formatGroundAircraft(ac *adsb.Aircraft, airport AirportInfo) string {
	var builder strings.Builder

	// 基本信息 - 呼号和运营人
	callsign := ac.Flight
	if callsign == "" {
		callsign = "Unknown"
	}
	airline := ac.Airline
	if airline == "" {
		airline = "N/A"
	}
	operator := "N/A"
	if ac.BSDB != nil && strings.TrimSpace(ac.BSDB.RegisteredOwners) != "" {
		operator = strings.TrimSpace(ac.BSDB.RegisteredOwners)
	}
	typeCode := "N/A"
	if ac.BSDB != nil {
		if strings.TrimSpace(ac.BSDB.Type) != "" {
			typeCode = strings.TrimSpace(ac.BSDB.Type)
		} else if strings.TrimSpace(ac.BSDB.ICAOTypeCode) != "" {
			typeCode = strings.TrimSpace(ac.BSDB.ICAOTypeCode)
		}
	}
	if typeCode == "N/A" && ac.ADSB != nil && strings.TrimSpace(ac.ADSB.AircraftType) != "" {
		typeCode = strings.TrimSpace(ac.ADSB.AircraftType)
	}

	builder.WriteString(fmt.Sprintf("%s", callsign))
	builder.WriteString(fmt.Sprintf(" | Airline: %s | Operator: %s | Type: %s | ", airline, operator, typeCode))

	// 尾流类别
	if ac.ADSB != nil && ac.ADSB.Category != "" {
		builder.WriteString(fmt.Sprintf("Wake Category: %s | ", ac.ADSB.Category))
	}

	// 飞行参数
	if ac.ADSB != nil {
		builder.WriteString("Flight params: ")

		// 如果可用,使用磁航向(0-360° 都有效),否则回退到航迹
		if ac.ADSB.MagHeading != nil && *ac.ADSB.MagHeading >= 0 && *ac.ADSB.MagHeading <= 360 {
			builder.WriteString(fmt.Sprintf("HDG: %.0f", *ac.ADSB.MagHeading))
		} else if ac.ADSB.Track != nil && *ac.ADSB.Track != 0 {
			builder.WriteString(fmt.Sprintf("HDG: %.0f", *ac.ADSB.Track))
		}

		if ac.ADSB.TAS != nil {
			builder.WriteString(fmt.Sprintf(", TAS: %.0f kts", *ac.ADSB.TAS))
		}

		if ac.ADSB.GS != nil {
			builder.WriteString(fmt.Sprintf(", GS: %.0f kts", *ac.ADSB.GS))
		}

		if ac.ADSB.AltBaro.Float64() != 0 {
			builder.WriteString(fmt.Sprintf(", alt: %.0f ft", ac.ADSB.AltBaro.Float64()))
		}

		if ac.ADSB.BaroRate != nil {
			builder.WriteString(fmt.Sprintf(", VS: %.0f fpm", *ac.ADSB.BaroRate))
		}

		// 如果可用,添加应答机编码
		if ac.ADSB.Squawk != "" {
			builder.WriteString(fmt.Sprintf(", squawk: %s", ac.ADSB.Squawk))
		}

		builder.WriteString(", status: on ground")

		// 添加起飞时间 - 始终显示,如果未知则使用 N/A
		if ac.DateTookoff != nil {
			timeSince := time.Since(*ac.DateTookoff)
			builder.WriteString(fmt.Sprintf(", T/O: %s ago", formatDuration(timeSince)))
		} else {
			builder.WriteString(", T/O: N/A")
		}
	}

	if ac.ADSB != nil && ac.ADSB.ATCDerived != nil && ac.ADSB.ATCDerived.ETAStationSec != nil {
		builder.WriteString(fmt.Sprintf(" | ATC Derived: ETA to Station: %s", formatEtaToStation(*ac.ADSB.ATCDerived.ETAStationSec)))
	}

	// 飞行阶段
	if ac.Phase != nil && len(ac.Phase.Current) > 0 {
		currentPhase := ac.Phase.Current[0]
		fullPhaseName := getFullPhaseName(currentPhase.Phase)
		timeSince := time.Since(currentPhase.Timestamp)
		builder.WriteString(fmt.Sprintf(" | Phase: %s (%s)", fullPhaseName, formatDuration(timeSince)))
	}

	// 遥测状态
	builder.WriteString(fmt.Sprintf(" | Telemetry: %s", ac.Status))
	if !ac.LastSeen.IsZero() {
		timeSince := time.Since(ac.LastSeen)
		builder.WriteString(fmt.Sprintf(", Last seen: %s", formatDuration(timeSince)))
	}

	return builder.String()
}

// getFullPhaseName 将阶段代码转换为完整名称
func getFullPhaseName(phase string) string {
	switch phase {
	case "NEW":
		return "New"
	case "TAX":
		return "Taxiing"
	case "T/O":
		return "Takeoff"
	case "DEP":
		return "Departure"
	case "CRZ":
		return "Cruise"
	case "ARR":
		return "Arrival"
	case "APP":
		return "Approach"
	case "T/D":
		return "Touchdown"
	default:
		return phase // 如果无法识别则返回原值
	}
}

// FormatWeatherData 格式化气象数据用于模板渲染
func FormatWeatherData(weather *weather.WeatherData) string {
	if weather == nil {
		return "Weather data not available."
	}

	var builder strings.Builder

	// 提取最新 METAR - 仅显示已解码的,不显示原始的
	if weather.METAR != nil {
		if metarMap, ok := weather.METAR.(map[string]interface{}); ok {
			if trend, exists := metarMap["trend"]; exists {
				if trendSlice, ok := trend.([]interface{}); ok && len(trendSlice) > 0 {
					// 获取最新 METAR(趋势数组中的第一个)
					if latestMetar, ok := trendSlice[0].(map[string]interface{}); ok {
						if txt, exists := latestMetar["txt"]; exists {
							if txtSlice, ok := txt.([]interface{}); ok && len(txtSlice) > 0 {
								builder.WriteString(fmt.Sprintf("Current Weather: %v\n", txtSlice[0]))
							}
						}
					}
				}
			}
		}
	}

	// TAF 摘要(保留但简化)
	if weather.TAF != nil {
		if tafMap, ok := weather.TAF.(map[string]interface{}); ok {
			tafText := firstNonEmptyString(tafMap["taf"], tafMap["decoded"], tafMap["raw"])
			if tafText != "" {
				builder.WriteString(fmt.Sprintf("TAF: %s\n", tafText))
			} else {
				builder.WriteString("TAF: Terminal forecast available\n")
			}
		}
	}

	// 最后更新
	if !weather.LastUpdated.IsZero() {
		timeSince := time.Since(weather.LastUpdated)
		builder.WriteString(fmt.Sprintf("Last updated: %s ago\n", formatDuration(timeSince)))
	}

	return builder.String()
}

// FormatRunwayData 格式化跑道数据用于模板渲染
func FormatRunwayData(runways []RunwayInfo) string {
	if len(runways) == 0 {
		return "Runway information not available."
	}

	var builder strings.Builder

	for _, runway := range runways {
		builder.WriteString(fmt.Sprintf("• Runway %s", runway.Name))
		if runway.LengthFt > 0 {
			builder.WriteString(fmt.Sprintf(" (%d ft)", runway.LengthFt))
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

// FormatActiveRunwaysData 格式化检测到的活动跑道用于模板渲染
func FormatActiveRunwaysData(scores []adsb.RunwayScore) string {
	if len(scores) == 0 {
		return "No active runway detected."
	}

	var builder strings.Builder
	builder.WriteString("DETECTED ACTIVE RUNWAY(S):\n")
	for _, score := range scores {
		builder.WriteString(fmt.Sprintf("• %s (%.0f%% confidence)\n", score.RunwayEnd, score.Probability*100.0))
	}

	return strings.TrimSpace(builder.String())
}

// FormatTranscriptionHistory 格式化最近通信用于模板渲染
func FormatTranscriptionHistory(communications []TranscriptionSummary) string {
	if len(communications) == 0 {
		return "No recent radio communications available."
	}

	var builder strings.Builder
	builder.WriteString("RECENT RADIO COMMUNICATIONS:\n\n")

	for _, comm := range communications {
		timeSince := time.Since(comm.Timestamp)
		builder.WriteString(fmt.Sprintf("• [%s ago] %s", formatDuration(timeSince), comm.Frequency))
		if comm.Speaker != "" {
			builder.WriteString(fmt.Sprintf(" (%s)", comm.Speaker))
		}
		if comm.Callsign != "" {
			builder.WriteString(fmt.Sprintf(" [%s]", comm.Callsign))
		}
		builder.WriteString(fmt.Sprintf(": %s\n", comm.Content))
	}

	return builder.String()
}

// FormatAirportData 格式化机场信息用于模板渲染
func FormatAirportData(airport AirportInfo) string {
	var builder strings.Builder
	builder.WriteString("AIRPORT INFORMATION:\n\n")
	builder.WriteString(fmt.Sprintf("• %s (%s)\n", airport.Name, airport.Code))
	if len(airport.Coordinates) >= 2 {
		builder.WriteString(fmt.Sprintf("• Coordinates: %.4f°, %.4f°\n", airport.Coordinates[0], airport.Coordinates[1]))
	}
	if airport.ElevationFt > 0 {
		builder.WriteString(fmt.Sprintf("• Elevation: %d ft MSL\n", airport.ElevationFt))
	}
	return builder.String()
}

// formatDuration 以人类可读的方式格式化持续时间
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	} else {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
}

func formatEtaToStation(seconds float64) string {
	totalSec := int(seconds + 0.5)
	if totalSec < 0 {
		totalSec = 0
	}
	minutes := totalSec / 60
	secs := totalSec % 60
	return fmt.Sprintf("%dm %ds", minutes, secs)
}
