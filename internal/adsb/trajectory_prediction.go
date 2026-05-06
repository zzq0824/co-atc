package adsb

import (
	"math"
	"time"
)

// ─── 轨迹预测 ──────────────────────────────────────────────────────────
//
// 为飞行器轨迹提供后报(向后外推)与预报(向前外推)。
// 两者都使用轨迹环形缓冲区与 DerivedState 做统计意义上的预测。
//
// 后报:在首次 ADS-B 观测之前,把航迹向前延伸约 60 秒,回答
// "在我们看到之前这架飞行器在哪里?"的问题。在累积足够数据
// (≥5 个有效点,R² ≥ 0.80)时计算一次,然后锁定。使用对位置
// 分量的 OLS 线性回归。
//
// 预报:基于带加速度与转弯率(取自 DerivedState)的运动学模型,
// 把航迹向前延伸 2 分钟。取代了原来朴素的恒定航向/恒定速度的
// PredictFuturePositions()。

const (
	// 后报:6 个点 × 10s = 向后 60 秒
	hindcastSteps     = 6
	hindcastStepSec   = 10.0
	hindcastMinPoints = 5    // 尝试后报所需的最少有效点数
	hindcastMaxPoints = 10   // 用于后报回归的最大点数
	hindcastMinR2     = 0.80 // 后报所需的最低 R² 置信度
	hindcastLockAfter = 30.0 // 跟踪超过这么多秒后锁定后报
	hindcastDecayBase = 0.95 // 后报点每步置信度衰减系数

	// 预报:12 个点 × 5s = 向前 1 分钟
	forecastSteps     = 12
	forecastStepSec   = 5.0
	forecastDecayBase = 0.99 // 预报点每步置信度衰减系数

	turnPenaltyMaxRate = 3.0         // 转弯惩罚降为 0 时的度/秒
	kmPerNM            = 1.852       // 每海里对应的公里数
	degPerKmLat        = 1.0 / 111.0 // 每公里纬度对应的度数(近似)
)

// PredictionPoint 表示一个带置信度的预测位置。
type PredictionPoint struct {
	Lat        float64   `json:"lat"`
	Lon        float64   `json:"lon"`
	Altitude   float64   `json:"altitude"`
	Heading    float64   `json:"heading"`
	Speed      float64   `json:"speed"`      // 地速(节)
	Confidence float64   `json:"confidence"` // 0.0 - 1.0
	Timestamp  time.Time `json:"timestamp"`
}

// TrajectoryPrediction 保存某一架飞行器计算得到的后报与预报。
type TrajectoryPrediction struct {
	Hindcast       []PredictionPoint // 过去的预测(最旧的在前,先于首次观测)
	Forecast       []PredictionPoint // 未来的预测(最近的在前,晚于最新观测)
	HindcastLocked bool              // 一旦锁定就不再重算
	ComputedAt     time.Time
}

// ─── 后报(向后外推) ──────────────────────────────────────────

// computeHindcast 使用对位置分量的 OLS 线性回归,从最早观测开始
// 把轨迹向后外推。
//
// 算法:
// 1. 收集最早的有效快照(最多 hindcastMaxPoints 个)
// 2. 对 lat(t)、lon(t)、alt(t) 做 OLS 回归
// 3. 计算 lat 与 lon 的 R² —— 两者都必须 ≥ hindcastMinR2
// 4. 应用转弯惩罚和数据密度系数,得到综合置信度
// 5. 若置信度 ≥ 0.80,则向后外推 6 步,每步 10 秒
// 6. 一旦计算完成或跟踪超过 hindcastLockAfter 秒,即锁定后报
func computeHindcast(at *AircraftTrajectory, derived *DerivedState) {
	pred := &at.Prediction
	if pred.HindcastLocked {
		return
	}

	// 按从旧到新收集有效快照
	var validSnaps []TrajectorySnapshot
	at.ForEachSnapshot(func(snap *TrajectorySnapshot) {
		if snap.Valid {
			validSnaps = append(validSnaps, *snap)
		}
	})

	if len(validSnaps) < hindcastMinPoints {
		return
	}

	// 检查是否应锁定(已经过了过多时间)
	earliest := validSnaps[0]
	latest := validSnaps[len(validSnaps)-1]
	trackingDuration := latest.Timestamp.Sub(earliest.Timestamp).Seconds()

	// 使用最早的点做回归(它们距离外推目标最近)
	n := len(validSnaps)
	if n > hindcastMaxPoints {
		n = hindcastMaxPoints
	}
	regSnaps := validSnaps[:n]

	// 对 lat、lon、alt 与时间做 OLS 回归
	r2Lat := olsR2(regSnaps, func(s TrajectorySnapshot) float64 { return s.Lat })
	r2Lon := olsR2(regSnaps, func(s TrajectorySnapshot) float64 { return s.Lon })

	slopeLat := olsSlope(regSnaps, func(s TrajectorySnapshot) float64 { return s.Lat })
	slopeLon := olsSlope(regSnaps, func(s TrajectorySnapshot) float64 { return s.Lon })
	slopeAlt := olsSlope(regSnaps, func(s TrajectorySnapshot) float64 { return s.AltBaro })

	// 用回归斜率为预测点计算航向
	headingRad := math.Atan2(slopeLon, slopeLat)
	heading := math.Mod(headingRad*180.0/math.Pi+360, 360)

	// 用回归斜率计算速度(度/秒 → 节)
	// lat 斜率单位为 度/秒,lon 斜率单位为 度/秒
	latKmPerSec := slopeLat * 111.0
	lonKmPerSec := slopeLon * 111.0 * math.Cos(earliest.Lat*math.Pi/180.0)
	speedKmPerSec := math.Sqrt(latKmPerSec*latKmPerSec + lonKmPerSec*lonKmPerSec)
	speedKts := speedKmPerSec * 3600.0 / kmPerNM

	// 综合置信度
	r2Min := math.Min(r2Lat, r2Lon)

	// 转弯惩罚:直飞 = 1.0,转弯 ≥3°/秒 = 0.0
	turnRate := 0.0
	if derived != nil {
		turnRate = math.Abs(derived.TrackRateDegPerSec)
	}
	turnPenalty := math.Max(0, 1.0-turnRate/turnPenaltyMaxRate)

	// 数据密度系数:5 点 = 0.5,10+ 点 = 1.0
	dataDensity := math.Min(1.0, float64(len(regSnaps))/float64(hindcastMaxPoints))

	confidence := r2Min * turnPenalty * dataDensity

	// 超过阈值时间后无论置信度如何都锁定
	if trackingDuration >= hindcastLockAfter {
		pred.HindcastLocked = true
		if confidence < hindcastMinR2 {
			// 置信度不够 —— 不带后报地锁定
			pred.Hindcast = nil
			return
		}
	}

	if confidence < hindcastMinR2 {
		return // 还没准备好,下个周期再试
	}

	// 从最早观测向后生成后报点
	points := make([]PredictionPoint, hindcastSteps)
	for i := 0; i < hindcastSteps; i++ {
		stepsBack := float64(hindcastSteps - i) // 6, 5, 4, 3, 2, 1(最旧的在前)
		dt := -stepsBack * hindcastStepSec      // 相对最早观测的负时间偏移

		lat := earliest.Lat + slopeLat*dt
		lon := earliest.Lon + slopeLon*dt
		alt := earliest.AltBaro + slopeAlt*dt
		if alt < 0 {
			alt = 0
		}

		pointConf := confidence * math.Pow(hindcastDecayBase, stepsBack)

		points[i] = PredictionPoint{
			Lat:        lat,
			Lon:        lon,
			Altitude:   alt,
			Heading:    heading,
			Speed:      speedKts,
			Confidence: pointConf,
			Timestamp:  earliest.Timestamp.Add(time.Duration(dt * float64(time.Second))),
		}
	}

	pred.Hindcast = points
	pred.HindcastLocked = true
}

// ─── 预报(向前外推) ───────────────────────────────────────────

// computeForecast 使用带加速度与转弯率的运动学模型,把轨迹从最新
// 观测向前外推。
//
// 使用 DerivedState 趋势进行基于物理的预测:
// - 速度按 GSAccelKtsPerSec 变化(钳制在合理范围内)
// - 航向按 TrackRateDegPerSec 变化(曲线路径)
// - 高度按 AltTrendFPM 变化(由 OLS 得到,比瞬时 VR 更稳定)
//
// 置信度按步衰减,转弯/加速越剧烈衰减越快。
func computeForecast(at *AircraftTrajectory, derived *DerivedState) {
	pred := &at.Prediction
	latest := at.Latest()
	if latest == nil || !latest.Valid {
		pred.Forecast = nil
		return
	}

	if derived == nil || derived.ValidPointCount < 2 {
		// 数据不足,无法进行基于轨迹的预报
		pred.Forecast = nil
		return
	}

	// 起始状态来自 DerivedState(平滑后)+ 最新位置
	curLat := latest.Lat
	curLon := latest.Lon
	curAlt := derived.AltMean
	curSpeed := derived.GroundSpeedKts
	curHeading := derived.TrackDeg

	// 趋势
	gsAccel := derived.GSAccelKtsPerSec
	trackRate := derived.TrackRateDegPerSec
	altRate := derived.AltTrendFPM / 60.0 // 把 fpm 转换为 ft/sec

	// 置信度系数
	turnFactor := math.Max(0.3, 1.0-math.Abs(trackRate)*0.15)
	accelFactor := math.Max(0.5, 1.0-math.Abs(gsAccel)*0.1)

	points := make([]PredictionPoint, forecastSteps)
	now := time.Now().UTC()

	for i := 0; i < forecastSteps; i++ {
		step := float64(i + 1)
		dt := step * forecastStepSec // 距离现在的秒数

		// 带加速度的速度,经过钳制
		speed := curSpeed + gsAccel*dt
		if speed < 0 {
			speed = 0
		}
		maxSpeed := curSpeed * 2.0
		if maxSpeed < 100 {
			maxSpeed = 100
		}
		if speed > maxSpeed {
			speed = maxSpeed
		}

		// 带转弯率的航向
		heading := curHeading + trackRate*dt
		heading = math.Mod(heading+360, 360)

		// 该步内的飞行距离(相对上一位置,而非起点)
		// 在该步区间内使用平均速度(梯形积分)
		prevDt := (step - 1) * forecastStepSec
		prevSpeed := curSpeed + gsAccel*prevDt
		if prevSpeed < 0 {
			prevSpeed = 0
		}
		avgSpeed := (prevSpeed + speed) / 2.0
		distKm := avgSpeed * kmPerNM / 3600.0 * forecastStepSec

		// 该步区间内用于位置积分的平均航向
		prevHeading := curHeading + trackRate*prevDt
		avgHeadingRad := (prevHeading + heading) / 2.0 * math.Pi / 180.0

		// 用该步内的平均航向更新位置
		cosLat := math.Cos(curLat * math.Pi / 180.0)
		if cosLat < 0.01 {
			cosLat = 0.01
		}
		latChange := distKm * math.Cos(avgHeadingRad) * degPerKmLat
		lonChange := distKm * math.Sin(avgHeadingRad) * degPerKmLat / cosLat

		// 从起点累积的位置
		// 为了正确积分,累加增量
		if i == 0 {
			curLat += latChange
			curLon += lonChange
		} else {
			// 从上一预测位置更新
			curLat = points[i-1].Lat + latChange
			curLon = points[i-1].Lon + lonChange
		}

		// 高度
		alt := curAlt + altRate*dt
		if alt < 0 {
			alt = 0
		}

		// 置信度
		conf := math.Pow(turnFactor, step) * math.Pow(accelFactor, step) * math.Pow(forecastDecayBase, step)

		points[i] = PredictionPoint{
			Lat:        curLat,
			Lon:        curLon,
			Altitude:   alt,
			Heading:    heading,
			Speed:      speed,
			Confidence: conf,
			Timestamp:  now.Add(time.Duration(dt * float64(time.Second))),
		}
	}

	pred.Forecast = points
	pred.ComputedAt = now
}

// ─── 转换辅助函数 ─────────────────────────────────────────────────────

// PredictionPointsToPositions 把预测点转换为 API 使用的 Position 类型。
// 置信度不会被保留(Position 没有该字段)。
func PredictionPointsToPositions(points []PredictionPoint) []Position {
	if len(points) == 0 {
		return nil
	}
	positions := make([]Position, len(points))
	for i, p := range points {
		positions[i] = Position{
			Lat:         NumberPtr(p.Lat),
			Lon:         NumberPtr(p.Lon),
			Altitude:    NumberPtr(p.Altitude),
			SpeedGS:     NumberPtr(p.Speed),
			SpeedTrue:   NumberPtr(p.Speed),
			TrueHeading: NumberPtr(p.Heading),
			MagHeading:  NumberPtr(p.Heading),
			Timestamp:   p.Timestamp,
		}
	}
	return positions
}
