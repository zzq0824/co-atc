package adsb

import (
	"math"

	"github.com/yegors/co-atc/internal/physics"
)

// computeATCDerivedMetrics 基于飞行器原始数据与到站点距离计算 ATC 派生指标
// 包括航向偏差、风分量、航迹角、爬升梯度、转弯率以及预计到站时间等
func computeATCDerivedMetrics(target *ADSBTarget, distanceNm *float64) *ATCDerivedMetrics {
	if target == nil {
		return nil
	}

	derived := &ATCDerivedMetrics{}

	gs := NumberOrZero(target.GS)
	tas := NumberOrZero(target.TAS)
	isMoving := gs > 25 || tas > 25

	track, hasTrack := pickHeadingLikeValue(target.Track, isMoving)
	referenceHeading, headingSource, hasHeading := pickReferenceHeading(target, isMoving)

	if headingSource != "" {
		derived.HeadingSource = headingSource
	}

	if hasTrack && hasHeading {
		drift := physics.SignedAngleDiffDeg(track, referenceHeading)
		derived.TrackHeadingErrorDeg = floatPtr(drift)
	}

	windDir := NumberOrZero(target.WD)
	windSpeed := NumberOrZero(target.WS)
	if hasTrack && windDir >= 0 && windSpeed > 0 {
		headwind, crosswind := physics.WindComponents(track, windDir, windSpeed)
		derived.HeadTailwindKt = floatPtr(headwind)
		derived.CrosswindKt = floatPtr(crosswind)
	}

	verticalRate, hasVerticalRate := pickVerticalRate(target)
	if hasVerticalRate && gs > 1 {
		fpa := physics.FlightPathAngleDeg(verticalRate, gs)
		gradient := physics.ClimbGradientFtPerNm(verticalRate, gs)
		derived.FlightPathAngleDeg = floatPtr(fpa)
		derived.ClimbGradientFtNm = floatPtr(gradient)
	}

	trackRate := NumberOrZero(target.TrackRate)
	if math.Abs(trackRate) > 0 {
		derived.TurnRateDegSec = floatPtr(trackRate)
	}

	if distanceNm != nil && *distanceNm >= 0 && gs > 30 {
		etaSec := (*distanceNm / gs) * 3600.0
		derived.ETAStationSec = floatPtr(etaSec)
	}

	if isATCDerivedEmpty(derived) {
		return nil
	}

	return derived
}

// AttachATCDerivedMetrics 为飞行器附加 ATC 派生指标
func AttachATCDerivedMetrics(aircraft *Aircraft) {
	if aircraft == nil || aircraft.ADSB == nil {
		return
	}
	aircraft.ADSB.ATCDerived = computeATCDerivedMetrics(aircraft.ADSB, aircraft.Distance)
}

// pickReferenceHeading 选择参考航向,优先级:真航向 > 磁航向 > 航迹
func pickReferenceHeading(target *ADSBTarget, moving bool) (float64, string, bool) {
	if v, ok := pickHeadingLikeValue(target.TrueHeading, moving); ok {
		return v, "TRUE", true
	}
	if v, ok := pickHeadingLikeValue(target.MagHeading, moving); ok {
		return v, "MAG", true
	}
	if v, ok := pickHeadingLikeValue(target.Track, moving); ok {
		return v, "TRACK", true
	}
	return 0, "", false
}

// pickHeadingLikeValue 选取航向类数值;静止时不返回 0,避免与"未知"混淆
func pickHeadingLikeValue(value *float64, moving bool) (float64, bool) {
	if value == nil {
		return 0, false
	}
	v := *value
	if v > 0 && v < 360 {
		return v, true
	}
	if v == 0 && moving {
		return 0, true
	}
	return 0, false
}

// pickVerticalRate 选择垂直速率,优先气压速率,其次几何速率
func pickVerticalRate(target *ADSBTarget) (float64, bool) {
	baroRate := NumberOrZero(target.BaroRate)
	if baroRate != 0 {
		return baroRate, true
	}
	geomRate := NumberOrZero(target.GeomRate)
	if geomRate != 0 {
		return geomRate, true
	}
	return 0, false
}

// isATCDerivedEmpty 判断 ATC 派生指标是否为空(全部字段未填充)
func isATCDerivedEmpty(v *ATCDerivedMetrics) bool {
	if v == nil {
		return true
	}
	return v.HeadingSource == "" &&
		v.TrackHeadingErrorDeg == nil &&
		v.HeadTailwindKt == nil &&
		v.CrosswindKt == nil &&
		v.FlightPathAngleDeg == nil &&
		v.ClimbGradientFtNm == nil &&
		v.TurnRateDegSec == nil &&
		v.ETAStationSec == nil
}

// atcDerivedEqual 判断两个 ATC 派生指标是否等价
func atcDerivedEqual(a, b *ATCDerivedMetrics) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.HeadingSource == b.HeadingSource &&
		floatPtrEqual(a.TrackHeadingErrorDeg, b.TrackHeadingErrorDeg) &&
		floatPtrEqual(a.HeadTailwindKt, b.HeadTailwindKt) &&
		floatPtrEqual(a.CrosswindKt, b.CrosswindKt) &&
		floatPtrEqual(a.FlightPathAngleDeg, b.FlightPathAngleDeg) &&
		floatPtrEqual(a.ClimbGradientFtNm, b.ClimbGradientFtNm) &&
		floatPtrEqual(a.TurnRateDegSec, b.TurnRateDegSec) &&
		floatPtrEqual(a.ETAStationSec, b.ETAStationSec)
}

// floatPtr 返回 float64 值的指针
func floatPtr(v float64) *float64 {
	out := v
	return &out
}

// floatPtrEqual 判断两个 float64 指针所指向的值是否相等
func floatPtrEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
