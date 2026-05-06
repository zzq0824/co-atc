package adsb

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yegors/co-atc/internal/config"
)

// 航空计算常量
const (

	// 转换因子
	METERS_PER_NM  = 1852.0  // 每海里米数
	FEET_PER_NM    = 6076.12 // 每海里英尺数
	FEET_PER_METER = 3.28084 // 每米英尺数

	// 轨迹预测的速度调整常量
	SPEED_ADJUST_RANGE_NM = 10.0 // 速度调整适用的范围(海里)
	SPEED_ADJUST_PERCENT  = 0.25 // 最大速度调整(25%)
)

// ValidateSensorData 检测并校正 ADS-B 信号衰减。
//
// 当飞行器离开 ADS-B 接收器覆盖区时,信号在完全消失之前
// 通常会出现衰减。在真实数据中常见的模式:
//
//   - 高度降至 0 而地速保持(部分丢失)
//   - 所有数值同时降至 0(完全丢失)
//   - 速度降至 0 而高度保持(部分丢失)
//   - 位置(纬度/经度)降至 0 而其他字段保持
//
// 此函数将当前数值与上一周期存储的数值进行比较,
// 当下降模式与已知信号衰减(而非实际飞行状态变化)
// 匹配时,延续之前的数据。
//
// 三层校正,按优先级顺序检查:
//
//  1. 完全信号丢失 — 所有数值从明显空中状态降至零。
//     无论位置如何均无歧义。校正所有项。
//
//  2. 高数值下降 — 单一数值从超过可配置阈值
//     (例如,高度 >10,000 英尺,或在高空速度 >100 节)降至零。
//     在一个轮询周期内物理上不可能。无论位置如何均校正。
//
//  3. 位置感知下降 — 数值在飞行器远离站台时
//     从中等水平降至零。在机场附近,零高度或
//     零速度可能是合法的(降落、滑行、停放)。需要位置数据。
//
// 限制:
//   - 仅校正降至零的下降。非零故障(例如 37000→500)无法
//     与覆盖间隙期间合法下降区分,除非比较时间戳,
//     此函数无法访问时间戳。
//   - 当飞行器位置未知(纬度/经度 = 0)时,跳过基于距离的(第三层)
//     规则,以避免来自 Haversine(0,0,...)的错误校正。
func ValidateSensorData(currentTAS, currentGS, currentAlt, prevTAS, prevGS, prevAlt,
	aircraftLat, aircraftLon, stationLat, stationLon, airportRangeNM float64,
	config *config.FlightPhasesConfig) (float64, float64, float64) {

	correctedTAS := currentTAS
	correctedGS := currentGS
	correctedAlt := currentAlt

	// ── 第一层:完全信号丢失 ──────────────────────────────────────
	// 所有三个数值从飞行状态同时归零是无歧义的信号丢失,
	// 无论与机场的距离如何。这捕获了下面单个规则会遗漏的
	// 情况(例如机场附近的 3000 英尺/200 节,
	// 那里第三层距离规则不适用)。
	if currentAlt == 0 && currentTAS == 0 && currentGS == 0 {
		wasAirborne := prevAlt >= config.HighAltitudeOverrideFt ||
			(prevAlt > config.FlyingMinAltFt && (prevTAS >= config.FlyingMinTASKts || prevGS >= config.FlyingMinTASKts))
		if wasAirborne {
			return prevTAS, prevGS, prevAlt
		}
	}

	// ── 第三层规则的距离上下文 ──────────────────────────────
	// 仅当我们有真实位置数据时才有意义。当纬度/经度 = 0
	// (mode_s 或信号丢失)时,Haversine 产生误导性的大距离,
	// 这会错误地触发"远离机场"校正。
	hasPosition := aircraftLat != 0 || aircraftLon != 0
	farFromAirport := false
	if hasPosition {
		farFromAirport = MetersToNM(Haversine(aircraftLat, aircraftLon, stationLat, stationLon)) > airportRangeNM
	}

	// ── 高度:检测降至零 ─────────────────────────────────
	if currentAlt == 0 && prevAlt > 0 {
		if prevAlt >= config.ImpossibleAltDropThresholdFt {
			// 第二层:超过巡航阈值(默认 10,000 英尺)— 总是信号丢失
			correctedAlt = prevAlt
		} else if prevAlt > 1000 && farFromAirport {
			// 第三层:1,000–10,000 英尺远离机场 — 可能是信号丢失
			// (机场附近,alt=0 可能是合法的降落/滑行)
			correctedAlt = prevAlt
		}
	}

	// ── TAS:检测降至零 ──────────────────────────────────────
	if currentTAS == 0 && prevTAS > 0 {
		if prevTAS >= config.ImpossibleSpeedDropThresholdKts && prevAlt >= config.ImpossibleSpeedDropMinAltFt {
			// 第二层:高空快速飞行器(默认 >100 节,>5,000 英尺)
			correctedTAS = prevTAS
		} else if prevTAS > 42 && farFromAirport {
			// 第三层:中速远离机场
			correctedTAS = prevTAS
		}
	}

	// ── GS:检测降至零(与 TAS 相同的逻辑)───────────────────
	if currentGS == 0 && prevGS > 0 {
		if prevGS >= config.ImpossibleSpeedDropThresholdKts && prevAlt >= config.ImpossibleSpeedDropMinAltFt {
			// 第二层:高空快速飞行器
			correctedGS = prevGS
		} else if prevGS > 42 && farFromAirport {
			// 第三层:中速远离机场
			correctedGS = prevGS
		}
	}

	return correctedTAS, correctedGS, correctedAlt
}

// IsFlying 根据速度和高度判断飞行器是否被认为在飞行
// 如果 TAS(真空速)为 0,则使用地速(GS)作为备份
// 同时处理直升机的特殊情况(高海拔,低速度)
func IsFlying(tas, gs, altitude float64, config *config.FlightPhasesConfig) bool {
	// 如果 TAS 为 0,使用地速作为备份
	speed := tas
	if speed == 0 {
		speed = gs
	}

	// 高海拔覆盖:如果高度非常高,飞行器必定在飞行
	// 无论速度数据如何(处理巡航高度的错误 ADSB 速度数据)
	if altitude >= config.HighAltitudeOverrideFt {
		return true
	}

	// Mode S 飞行器:仅报告高度(无位置/速度)。当 TAS 和 GS
	// 都恰好为 0(无可用速度数据)时,仅使用高度作为飞行的证据。
	if tas == 0 && gs == 0 && altitude >= config.FlyingMinAltFt {
		return true
	}

	// 正常情况:速度和高度都高于阈值
	if speed >= config.FlyingMinTASKts && altitude >= config.FlyingMinAltFt {
		return true
	}

	// 直升机的特殊情况:高度至少为阈值的 helicopterMultiplier 倍,
	// 但速度大于阈值的一半
	if altitude >= (config.FlyingMinAltFt*config.HelicopterAltMultiplier) && speed > (config.FlyingMinTASKts/2) {
		return true
	}

	// 边缘情况:高速度和低/零高度可能表示
	// 传感器错误或飞行器离开 ADS-B 范围 - 优先速度而非高度
	if speed >= config.HighSpeedThresholdKts {
		return true
	}

	return false
}

// Haversine 计算两个纬度/经度点之间以米为单位的距离。
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000 // 地球半径(米)
	rad := math.Pi / 180.0

	lat1Rad := lat1 * rad
	lon1Rad := lon1 * rad
	lat2Rad := lat2 * rad
	lon2Rad := lon2 * rad

	dlon := lon2Rad - lon1Rad
	dlat := lat2Rad - lat1Rad

	a := math.Pow(math.Sin(dlat/2), 2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Pow(math.Sin(dlon/2), 2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return R * c
}

// CalculateBearing 计算从点 1 到点 2 的方位角(度)
// 返回 0 到 360 度之间的值(0 = 北,90 = 东,等等)
func CalculateBearing(lat1, lon1, lat2, lon2 float64) float64 {
	// 转换为弧度
	lat1Rad := lat1 * math.Pi / 180.0
	lon1Rad := lon1 * math.Pi / 180.0
	lat2Rad := lat2 * math.Pi / 180.0
	lon2Rad := lon2 * math.Pi / 180.0

	// 计算方位角
	y := math.Sin(lon2Rad-lon1Rad) * math.Cos(lat2Rad)
	x := math.Cos(lat1Rad)*math.Sin(lat2Rad) - math.Sin(lat1Rad)*math.Cos(lat2Rad)*math.Cos(lon2Rad-lon1Rad)
	bearing := math.Atan2(y, x) * 180.0 / math.Pi

	// 转换为 0-360 度
	bearing = math.Mod(bearing+360.0, 360.0)

	return bearing
}

// CalculateRelativeBearing 根据飞行器 1 的航向计算从飞行器 1 到飞行器 2 的相对方位角
// 返回 0 到 360 度之间的值。
// 这是相对于飞行器航向的标准航空"时钟位置"。
func CalculateRelativeBearing(lat1, lon1, heading1, lat2, lon2 float64) float64 {
	// 计算从飞行器 1 到飞行器 2 的绝对方位角
	absoluteBearing := CalculateBearing(lat1, lon1, lat2, lon2)

	// 计算相对方位角
	relativeBearing := absoluteBearing - heading1

	// 归一化到 0-360 度
	relativeBearing = math.Mod(relativeBearing+360.0, 360.0)

	return relativeBearing
}

// MetersToNM 将米转换为海里
func MetersToNM(meters float64) float64 {
	return meters / METERS_PER_NM
}

// NMToMeters 将海里转换为米
func NMToMeters(nm float64) float64 {
	return nm * METERS_PER_NM
}

// FeetToMeters 将英尺转换为米
func FeetToMeters(feet float64) float64 {
	return feet / FEET_PER_METER
}

// MetersToFeet 将米转换为英尺
func MetersToFeet(meters float64) float64 {
	return meters * FEET_PER_METER
}

// ParseCoordinates 将 "lat,lon" 格式的字符串解析为 float64 值
func ParseCoordinates(coordStr string) (float64, float64, error) {
	parts := strings.Split(coordStr, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("无效的坐标格式,预期 'lat,lon'")
	}

	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("无效的纬度:%w", err)
	}

	lon, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("无效的经度:%w", err)
	}

	return lat, lon, nil
}

// IsHexCode 检查字符串是否为有效的十六进制代码(ICAO 地址)
func IsHexCode(s string) bool {
	hexPattern := regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
	return hexPattern.MatchString(s)
}

// IsFlightNumber 检查字符串是否可能是航班号
func IsFlightNumber(s string) bool {
	// 大多数航班号是 2-3 个字母后跟 1-4 个数字
	flightPattern := regexp.MustCompile(`^[A-Za-z]{2,3}[0-9]{1,4}$`)
	return flightPattern.MatchString(s)
}

// IsTailNumber 检查字符串是否可能是机尾/注册号
func IsTailNumber(s string) bool {
	// 常见的机尾号模式:
	// N 号(美国):N 后跟 1-5 个数字或 1-4 个数字后跟 1-2 个字母
	// C-XXXX(加拿大):C- 后跟 4 个字符
	// G-XXXX(英国):G- 后跟 4 个字符
	tailPatterns := []*regexp.Regexp{
		regexp.MustCompile(`^N[0-9]{1,5}$`),              // N12345
		regexp.MustCompile(`^N[0-9]{1,4}[A-Za-z]{1,2}$`), // N123AB
		regexp.MustCompile(`^[A-Z]-[A-Z0-9]{4}$`),        // C-FKWZ, G-ABCD
		regexp.MustCompile(`^[A-Z]{2}-[A-Z0-9]{3,4}$`),   // VH-ABC, JA-8089
		regexp.MustCompile(`^[A-Z]{3}[0-9]{1,4}[A-Z]?$`), // 各种其他格式
	}

	for _, pattern := range tailPatterns {
		if pattern.MatchString(s) {
			return true
		}
	}

	return false
}

// --- ICAO 到机尾号转换函数 ---

// ICAO 到机尾号转换的常量
const (
	icaoSize = 6 // ICAO 十六进制地址的大小

	usCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ" // 不含 I 和 O 的字母表
	digitset  = "0123456789"

	caAlphabet      = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	caAlphabetLen   = 26
	caMax3LetterVal = caAlphabetLen * caAlphabetLen * caAlphabetLen // 17576(C-Fxxx 或 C-Gxxx 各)
)

var usAllChars = usCharset + digitset

// 美国转换的预计算常量
var (
	usSuffixSize  int
	usBucket4Size int
	usBucket3Size int
	usBucket2Size int
	usBucket1Size int

	usCharsetLen  int
	usDigitsetLen int
	usAllCharsLen int
)

func init() {
	// 初始化美国转换常量
	usCharsetLen = len(usCharset)
	usDigitsetLen = len(digitset)
	usAllCharsLen = len(usAllChars)

	usSuffixSize = 1 + usCharsetLen*(1+usCharsetLen)
	usBucket4Size = 1 + usCharsetLen + usDigitsetLen
	usBucket3Size = usDigitsetLen*usBucket4Size + usSuffixSize
	usBucket2Size = usDigitsetLen*usBucket3Size + usSuffixSize
	usBucket1Size = usDigitsetLen*usBucket2Size + usSuffixSize
}

// CleanFlightName 从航班名称中删除空白和空字符
func CleanFlightName(flight string) string {
	return strings.TrimSpace(strings.ReplaceAll(flight, "\\x00", ""))
}

// getSuffixUS 根据偏移量计算美国机尾号的后缀。
func getSuffixUS(offset int) string {
	if offset == 0 {
		return ""
	}
	char0Idx := (offset - 1) / (usCharsetLen + 1)
	if char0Idx < 0 || char0Idx >= usCharsetLen {
		return fmt.Sprintf("!ERR_IDX_%d!", char0Idx)
	}
	char0 := string(usCharset[char0Idx])

	rem := (offset - 1) % (usCharsetLen + 1)
	if rem == 0 {
		return char0
	}
	if rem-1 < 0 || rem-1 >= usCharsetLen {
		return fmt.Sprintf("!ERR_REM_IDX_%d!", rem-1)
	}
	return char0 + string(usCharset[rem-1])
}

// USIcaoToN 将美国 ICAO 地址转换为其 N 号。
func USIcaoToN(icaoUpper string) (string, error) {
	valHex := icaoUpper[1:]
	parsedVal, err := strconv.ParseInt(valHex, 16, 64)
	if err != nil {
		return "", fmt.Errorf("解析美国 ICAO 十六进制 '%s' 失败:%v", valHex, err)
	}
	idx := int(parsedVal) - 1

	if idx < 0 || idx > 915398 { // 有效美国范围:A00001 到 ADF7C7
		return "", fmt.Errorf("ICAO 值 %s(idx %d)超出美国 N 号映射的有效范围(A00001-ADF7C7)", icaoUpper, idx)
	}

	output := "N"
	dig1 := (idx / usBucket1Size) + 1
	rem1 := idx % usBucket1Size
	output += strconv.Itoa(dig1)

	if rem1 < usSuffixSize {
		return output + getSuffixUS(rem1), nil
	}

	rem1 -= usSuffixSize
	dig2 := rem1 / usBucket2Size
	rem2 := rem1 % usBucket2Size
	output += strconv.Itoa(dig2)

	if rem2 < usSuffixSize {
		return output + getSuffixUS(rem2), nil
	}

	rem2 -= usSuffixSize
	dig3 := rem2 / usBucket3Size
	rem3 := rem2 % usBucket3Size
	output += strconv.Itoa(dig3)

	if rem3 < usSuffixSize {
		return output + getSuffixUS(rem3), nil
	}

	rem3 -= usSuffixSize
	dig4 := rem3 / usBucket4Size
	rem4 := rem3 % usBucket4Size
	output += strconv.Itoa(dig4)

	if rem4 == 0 {
		return output, nil
	}
	if rem4-1 < 0 || rem4-1 >= usAllCharsLen {
		return "", fmt.Errorf("内部错误:usAllChars(长度 %d)的 rem4 索引 %d 无效", usAllCharsLen, rem4-1)
	}
	return output + string(usAllChars[rem4-1]), nil
}

// CAIcaoToN 将加拿大 ICAO 地址转换为其机尾号。
func CAIcaoToN(icaoUpper string) (string, error) {
	valHex := icaoUpper[1:]
	d, err := strconv.ParseInt(valHex, 16, 64)
	if err != nil {
		return "", fmt.Errorf("解析加拿大 ICAO 十六进制 '%s' 失败:%v", valHex, err)
	}

	var prefix string
	dEff := 0

	if d >= 1 && d <= caMax3LetterVal {
		prefix = "C-F"
		dEff = int(d) - 1
	} else if d >= (caMax3LetterVal+1) && d <= (caMax3LetterVal*2) {
		prefix = "C-G"
		dEff = int(d) - 1 - caMax3LetterVal
	} else {
		return "", fmt.Errorf("加拿大 ICAO 值 %s(十进制 %d)超出 C-Fxxx 或 C-Gxxx 映射的范围", icaoUpper, d)
	}

	if dEff < 0 || dEff >= caMax3LetterVal {
		return "", fmt.Errorf("内部错误:加拿大机尾字母的 dEff %d 超出预期范围 [0, %d)", dEff, caMax3LetterVal)
	}

	l1Idx := dEff % caAlphabetLen
	dEff /= caAlphabetLen
	l2Idx := dEff % caAlphabetLen
	dEff /= caAlphabetLen
	l3Idx := dEff % caAlphabetLen

	if l1Idx < 0 || l1Idx >= caAlphabetLen ||
		l2Idx < 0 || l2Idx >= caAlphabetLen ||
		l3Idx < 0 || l3Idx >= caAlphabetLen {
		return "", fmt.Errorf("内部错误:加拿大机尾字母的计算字母索引超出范围")
	}

	tailLetters := string(caAlphabet[l3Idx]) + string(caAlphabet[l2Idx]) + string(caAlphabet[l1Idx])
	return prefix + tailLetters, nil
}

// IcaoToTailNumber 将 ICAO 十六进制地址转换为机尾号。
func IcaoToTailNumber(icao string) (string, error) {
	if len(icao) != icaoSize {
		return "", fmt.Errorf("ICAO 十六进制地址必须为 %d 个字符长,'%s' 得到 %d 个", icaoSize, len(icao), icao)
	}
	icaoUpper := strings.ToUpper(icao)

	for i := 1; i < icaoSize; i++ {
		isHex := false
		char := rune(icaoUpper[i])
		if (char >= '0' && char <= '9') || (char >= 'A' && char <= 'F') {
			isHex = true
		}
		if !isHex {
			return "", fmt.Errorf("ICAO 十六进制地址 '%s' 在位置 %d 包含非十六进制字符 '%c'", icao, i+1, icaoUpper[i])
		}
	}

	firstChar := icaoUpper[0]
	switch firstChar {
	case 'A':
		return USIcaoToN(icaoUpper)
	case 'C':
		return CAIcaoToN(icaoUpper)
	default:
		return "", fmt.Errorf("'%s' 中不支持的 ICAO 前缀 '%c'。仅支持 'A'(美国)和 'C'(加拿大)", icao, firstChar)
	}
}

// PredictFuturePositions 根据飞行器的当前位置、航向、速度和垂直速率
// 计算其预测的未来位置。
// 它返回一个数组,包含未来 5 分钟内每分钟间隔的预测位置。
// 该函数还根据距机场(站点)的接近程度调整速度。
func PredictFuturePositions(lat, lon, altBaro, trueHeading, magHeading, speedKnots, verticalRateFtMin float64) []Position {
	predictions := make([]Position, 5) // 5 个预测(未来 1-5 分钟)
	now := time.Now().UTC()

	// 将航向从度转换为弧度以进行三角计算
	headingRad := trueHeading * math.Pi / 180.0

	// 计算每分钟以度为单位的行进距离
	// 1 节 = 每小时 1 海里 = 每小时 1.852 公里
	// 1 分钟 = 1/60 小时
	// 每分钟公里距离 = speedKnots * 1.852 / 60
	speedKmPerMin := speedKnots * 1.852 / 60

	// 每公里近似度数(随纬度变化,但这是合理的近似)
	// 1 度纬度 = ~111 公里
	// 1 度经度 = ~111 公里 * cos(纬度)
	latKmPerDegree := 111.0
	lonKmPerDegree := 111.0 * math.Cos(lat*math.Pi/180.0)

	// 从配置中获取站点坐标
	// 目前,我们将使用占位符函数,稍后将替换为实际配置值
	stationLat, stationLon := GetStationCoordinates()

	// 以海里为单位计算到站点的初始距离(用于日志记录/调试)
	_ = Haversine(lat, lon, stationLat, stationLon) / METERS_PER_NM

	// 根据航向确定我们是接近还是离开站点
	// 计算到站点的方位角
	bearingToStation := Bearing(lat, lon, stationLat, stationLon)

	// 计算飞行器航向与到站点方位角之间的绝对角度差
	// 如果差值小于 90 度,飞行器朝向站点
	// 如果差值大于 90 度,飞行器远离站点
	headingDiff := math.Abs(trueHeading - bearingToStation)
	if headingDiff > 180 {
		headingDiff = 360 - headingDiff
	}

	approachingStation := headingDiff < 90

	for i := 0; i < 5; i++ {
		minutesAhead := float64(i + 1)

		// 从原始速度开始
		adjustedSpeed := speedKnots
		adjustedSpeedKmPerMin := speedKmPerMin

		// 计算新位置
		latChange := (adjustedSpeedKmPerMin * minutesAhead * math.Cos(headingRad)) / latKmPerDegree
		lonChange := (adjustedSpeedKmPerMin * minutesAhead * math.Sin(headingRad)) / lonKmPerDegree

		newLat := lat + latChange
		newLon := lon + lonChange

		// 计算预测位置到站点的距离
		predictedDistanceToStationNM := Haversine(newLat, newLon, stationLat, stationLon) / METERS_PER_NM

		// 如果在范围内,根据距机场的接近程度调整速度
		if predictedDistanceToStationNM < SPEED_ADJUST_RANGE_NM {
			// 根据我们距机场的接近程度计算调整因子(0-1)
			adjustmentFactor := (SPEED_ADJUST_RANGE_NM - predictedDistanceToStationNM) / SPEED_ADJUST_RANGE_NM

			// 根据我们是接近还是离开应用调整
			if approachingStation {
				// 接近时降低速度
				adjustedSpeed = speedKnots * (1.0 - (SPEED_ADJUST_PERCENT * adjustmentFactor))
			} else {
				// 离开时提高速度
				adjustedSpeed = speedKnots * (1.0 + (SPEED_ADJUST_PERCENT * adjustmentFactor))
			}
		}

		// 根据垂直速率计算新高度
		// 垂直速率以每分钟英尺为单位
		newAltitude := altBaro + (verticalRateFtMin * minutesAhead)

		// 如果我们正在接近站点且预测高度为负,
		// 将其调整为地面水平(0 英尺)
		if approachingStation && newAltitude < 0 {
			// 为 UI 警告目的保留负值,但限制为 -100 英尺
			// 这允许 UI 显示警告图标,同时防止极端负值
			if newAltitude < -100 {
				newAltitude = -100
			}
		}

		// 创建预测
		timestamp := now.Add(time.Duration(minutesAhead) * time.Minute)

		predictions[i] = Position{
			Lat:         NumberPtr(newLat),
			Lon:         NumberPtr(newLon),
			Altitude:    NumberPtr(newAltitude),
			SpeedTrue:   NumberPtr(adjustedSpeed),
			SpeedGS:     NumberPtr(adjustedSpeed),
			TrueHeading: NumberPtr(trueHeading), // 假设真航向恒定
			MagHeading:  NumberPtr(magHeading),  // 假设磁航向恒定
			Timestamp:   timestamp,
		}
	}

	return predictions
}

// GetStationCoordinates 从配置中返回站点(机场)的纬度和经度。
// 如果配置不可用,则返回默认值。
func GetStationCoordinates() (float64, float64) {
	// 从服务获取配置
	cfg := GetConfig()
	if cfg != nil && cfg.Station.Latitude != 0 && cfg.Station.Longitude != 0 {
		return cfg.Station.Latitude, cfg.Station.Longitude
	}

	// 默认为多伦多皮尔逊国际机场坐标
	return 43.6777, -79.6248
}

// GetConfig 返回当前配置
// 这是一个占位符,应替换为实际的配置访问
var configInstance *Config

func GetConfig() *Config {
	return configInstance
}

// Config 表示应用程序配置
type Config struct {
	Station struct {
		Latitude  float64
		Longitude float64
	}
}

// SetConfig 设置用于测试目的的配置
func SetConfig(cfg *Config) {
	configInstance = cfg
}

// Bearing 计算从点 1 到点 2 的初始方位角
func Bearing(lat1, lon1, lat2, lon2 float64) float64 {
	// 转换为弧度
	lat1 = lat1 * math.Pi / 180.0
	lon1 = lon1 * math.Pi / 180.0
	lat2 = lat2 * math.Pi / 180.0
	lon2 = lon2 * math.Pi / 180.0

	// 计算方位角
	y := math.Sin(lon2-lon1) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(lon2-lon1)
	bearing := math.Atan2(y, x) * 180.0 / math.Pi

	// 归一化到 0-360
	if bearing < 0 {
		bearing += 360.0
	}

	return bearing
}

// RunwayThreshold 表示带有坐标的单个跑道入口
type RunwayThreshold struct {
	ID        string  `json:"id"` // 例如,"05","23","06L","24R"
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// RunwayData 表示来自 runways.json 的跑道数据结构
type RunwayData struct {
	Airport          string                         `json:"airport"`
	RunwayThresholds map[string]map[string]struct { // 例如,"05-23" -> "05" -> {lat, lon}
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"runway_thresholds"`
}

// DetectRunwayApproach 判断飞行器是否正在接近任何跑道
func DetectRunwayApproach(lat, lon, heading, altitude float64, runways RunwayData, config config.FlightPhasesConfig) *RunwayApproachInfo {
	var bestApproach *RunwayApproachInfo
	minDistance := float64(config.ApproachMaxDistanceNM) + 1 // 从超过最大值的距离开始

	// 检查每个跑道入口
	for runwayPair, thresholds := range runways.RunwayThresholds {
		for thresholdID, threshold := range thresholds {
			// 计算到入口的距离
			distanceMeters := Haversine(lat, lon, threshold.Latitude, threshold.Longitude)
			distanceNM := MetersToNM(distanceMeters)

			// 如果距入口太远则跳过
			if distanceNM > float64(config.ApproachMaxDistanceNM) {
				continue
			}

			// 计算跑道航向 — 飞行器使用此跑道端时飞行的方向
			// (从此入口朝向对面入口)。
			// 接近跑道 05 的飞行器以 ~050° 航向飞行,这与
			// 从入口 05 到入口 23 的方位角匹配。
			var runwayHeading float64
			oppositeThresholdID := getOppositeThreshold(thresholdID, runwayPair)
			if oppositeThreshold, exists := thresholds[oppositeThresholdID]; exists {
				runwayHeading = CalculateBearing(threshold.Latitude, threshold.Longitude,
					oppositeThreshold.Latitude, oppositeThreshold.Longitude)
			} else {
				// 如果找不到对面入口,跳过这个
				continue
			}

			// 计算航向对齐
			headingDiff := math.Abs(heading - runwayHeading)
			if headingDiff > 180 {
				headingDiff = 360 - headingDiff
			}

			// 如果航向不对齐则跳过
			if headingDiff > float64(config.ApproachHeadingToleranceDeg) {
				continue
			}

			// 计算距跑道中线的距离
			runwayThreshold := RunwayThreshold{
				ID:        thresholdID,
				Latitude:  threshold.Latitude,
				Longitude: threshold.Longitude,
			}
			centerlineDistance := CalculateRunwayCenterlineDistance(lat, lon, runwayThreshold, runwayHeading)

			// 检查是否在中线容差范围内
			if centerlineDistance <= config.ApproachCenterlineToleranceNM {
				// 这是一个有效的接近 - 检查它是否最近
				if distanceNM < minDistance {
					minDistance = distanceNM
					bestApproach = &RunwayApproachInfo{
						RunwayID:               runwayPair + "/" + thresholdID,
						DistanceToThreshold:    distanceNM,
						DistanceFromCenterline: centerlineDistance,
						HeadingAlignment:       headingDiff,
						OnApproach:             true,
					}
				}
			}
		}
	}

	return bestApproach
}

// CalculateRunwayCenterlineDistance 计算飞行器到跑道中线的距离
func CalculateRunwayCenterlineDistance(aircraftLat, aircraftLon float64, threshold RunwayThreshold, runwayHeading float64) float64 {
	// 计算从入口到飞行器的方位角
	bearingToAircraft := CalculateBearing(threshold.Latitude, threshold.Longitude, aircraftLat, aircraftLon)

	// 计算从入口到飞行器的距离
	distanceToAircraft := MetersToNM(Haversine(threshold.Latitude, threshold.Longitude, aircraftLat, aircraftLon))

	// 计算跑道航向与到飞行器方位角之间的角度
	angleDiff := math.Abs(runwayHeading - bearingToAircraft)
	if angleDiff > 180 {
		angleDiff = 360 - angleDiff
	}

	// 计算垂直距离(距中线的距离)
	// 使用正弦定理:垂直距离 = 斜边 * sin(角度)
	centerlineDistance := distanceToAircraft * math.Sin(angleDiff*math.Pi/180.0)

	return centerlineDistance
}

// IsOnRunwayApproach 判断飞行器是否符合特定跑道的接近标准
func IsOnRunwayApproach(aircraftLat, aircraftLon, heading, altitude float64, threshold RunwayThreshold, config config.FlightPhasesConfig) bool {
	// 计算到入口的距离
	distanceMeters := Haversine(aircraftLat, aircraftLon, threshold.Latitude, threshold.Longitude)
	distanceNM := MetersToNM(distanceMeters)

	// 检查距离约束
	if distanceNM > float64(config.ApproachMaxDistanceNM) {
		return false
	}

	// 对于接近阶段,我们还需要低于一定高度(根据计划为 5000 英尺)
	if altitude > 5000 {
		return false
	}

	// 计算跑道航向(此处简化 - 在实际实现中我们需要对面入口)
	// 目前,我们假设跑道航向可用
	// 这需要使用实际跑道数据进行增强

	return true
}

// getOppositeThreshold 返回给定入口的对面入口 ID
func getOppositeThreshold(thresholdID, runwayPair string) string {
	// 拆分跑道对(例如,"05-23" -> ["05", "23"])
	parts := strings.Split(runwayPair, "-")
	if len(parts) != 2 {
		return ""
	}

	// 返回对面的入口
	if thresholdID == parts[0] {
		return parts[1]
	}
	return parts[0]
}

// DetectRunwayDeparture 判断飞行器是否从任何跑道离港
// 与接近检测不同,离港检测更宽松:
// - 飞行器不需要在中线上(它们在起飞后迅速偏离)
// - 我们检查飞行器是否在远离机场/跑道
// - 距离容差较大,因为飞行器在离港后会分散
func DetectRunwayDeparture(lat, lon, heading float64, runways RunwayData, stationLat, stationLon float64, config config.FlightPhasesConfig) *RunwayDepartureInfo {
	var bestDeparture *RunwayDepartureInfo
	minDistance := float64(config.ApproachMaxDistanceNM) + 1 // 从超过最大值的距离开始

	// 检查每个跑道入口
	for runwayPair, thresholds := range runways.RunwayThresholds {
		for thresholdID, threshold := range thresholds {
			// 计算距入口的距离
			distanceMeters := Haversine(lat, lon, threshold.Latitude, threshold.Longitude)
			distanceNM := MetersToNM(distanceMeters)

			// 如果距入口太远则跳过(离港使用较大容差)
			maxDepartureDistance := float64(config.ApproachMaxDistanceNM) * 1.5 // 接近距离的 1.5 倍
			if distanceNM > maxDepartureDistance {
				continue
			}

			// 计算跑道航向(从此入口向外)
			var runwayHeading float64
			oppositeThresholdID := getOppositeThreshold(thresholdID, runwayPair)
			if oppositeThreshold, exists := thresholds[oppositeThresholdID]; exists {
				runwayHeading = CalculateBearing(threshold.Latitude, threshold.Longitude,
					oppositeThreshold.Latitude, oppositeThreshold.Longitude)
			} else {
				// 如果找不到对面入口,跳过这个
				continue
			}

			// 计算航向对齐(对离港更宽松)
			headingDiff := math.Abs(heading - runwayHeading)
			if headingDiff > 180 {
				headingDiff = 360 - headingDiff
			}

			// 离港的航向容差更宽松(飞行器迅速偏离)
			departureHeadingTolerance := float64(config.ApproachHeadingToleranceDeg) * 2.0 // 接近容差的 2 倍
			if headingDiff <= departureHeadingTolerance {
				// 检查飞行器是否远离站点
				// 计算从飞行器到站点的方位角
				bearingToStation := CalculateBearing(lat, lon, stationLat, stationLon)

				// 如果飞行器航向与到站点方位角大致相反,则它在远离
				awayHeadingDiff := math.Abs(heading - bearingToStation)
				if awayHeadingDiff > 180 {
					awayHeadingDiff = 360 - awayHeadingDiff
				}

				// 如果航向与站点方位角大致相反,飞行器在远离
				// 允许 90 度容差(飞行器可以垂直移动并仍然在离港)
				if awayHeadingDiff >= 90 {
					// 这是一个有效的离港 - 检查它是否最近
					if distanceNM < minDistance {
						minDistance = distanceNM
						bestDeparture = &RunwayDepartureInfo{
							RunwayID:              runwayPair + "/" + thresholdID,
							DistanceFromThreshold: distanceNM,
							HeadingAlignment:      headingDiff,
							OnDeparture:           true,
						}
					}
				}
			}
		}
	}

	return bestDeparture
}
