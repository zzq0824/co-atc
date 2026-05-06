package adsb

import (
	"math"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// ─── 阶段检测 ──────────────────────────────────────────────────────────
//
// DeterminePhase 取代了旧的单点 determineFlightPhase() 函数。
// 它分析轨迹窗口,把飞行器分类到 10 个阶段之一:
//
//   NEW — 新发现或在地面静止(无数据或停场)
//   TAX — 地面滑行(低速移动)
//   T/O — 起飞(由上游 detectGroundStateTransitions 处理,而非在此处)
//   CLB — 从本机场初始爬升(跑道航向、加速、靠近机场)
//   DEP — 离场/一般爬升(在巡航以下爬升,远离台站)
//   CRZ — 巡航/航路(在 FL180 及以上)
//   ARR — 进场(靠近台站,在巡航以下,下降或平飞)
//   APP — 进近(在五边、不爬升、跑道对齐、靠近台站)
//   T/D — 接地(由上游 detectGroundStateTransitions 处理,而非在此处)
//   UNK — 未知(在空中但不匹配任何特定条件)
//
// CLB 与 DEP 在离场侧与 APP/ARR 对镜像:
//   CLB = 从本机场初始爬升(跑道航迹、加速、靠近机场) —— 类似 APP
//   DEP = 远离台站的一般离场爬升(任何向外爬升的流量) —— 类似 ARR
//
// T/O 与 T/D 由优先级 1 的地面状态转换系统
// (detectGroundStateTransitions)检测,本函数永远不会输出它们。
//
// 规则按严格的优先级顺序求值。第一条匹配的规则胜出。

// DeterminePhase 返回为给定飞行器检测到的飞行阶段。
// 它使用基于轨迹派生的状态,实现健壮、抗噪声的分类。
// 当轨迹数据不足(少于 MinPointsForAnalysis)时,会基于可用数据点
// 应用简单的启发式规则。
func (tt *TrajectoryTracker) DeterminePhase(
	aircraft *Aircraft,
	currentPhase *PhaseChange,
	takeoffTime *time.Time,
) string {
	// ── 规则 0:无数据 ──────────────────────────────────────────────
	if aircraft.ADSB == nil {
		return "NEW"
	}

	// 确保派生状态是最新的
	derived := tt.EnsureDerived(aircraft.Hex)

	// 如果完全没有轨迹数据(第一次观测),则基于唯一可用的数据点
	// 使用最小化的启发式规则。
	if derived == nil || derived.ValidPointCount == 0 {
		return tt.singlePointPhase(aircraft)
	}

	// ── 规则 1:地面阶段 ────────────────────────────────────────────
	if aircraft.OnGround {
		return tt.ruleGround(aircraft, currentPhase, derived)
	}

	// ── 空中规则(需要轨迹分析)─────────────────

	// 如果点数少于分析阈值,使用在数据有限情况下也可工作的简化
	// 空中启发式规则。
	if derived.ValidPointCount < tt.config.MinPointsForAnalysis {
		return tt.fewPointsAirbornePhase(aircraft, derived, currentPhase, takeoffTime)
	}

	// ── 完整的基于轨迹的规则 ──────────────────────────────────

	// 规则 2:巡航(空中最高优先级)
	if phase := tt.ruleCruise(derived); phase != "" {
		return phase
	}

	// 规则 3:进近(具体 —— 与跑道对齐的五边)
	if phase := tt.ruleApproach(aircraft, derived); phase != "" {
		return phase
	}

	// 规则 4:爬升(具体 —— 从本机场初始爬升)
	if phase := tt.ruleClimb(aircraft, derived, takeoffTime); phase != "" {
		return phase
	}

	// 规则 5:离场(一般 —— 远离台站爬升)
	if phase := tt.ruleDeparture(derived); phase != "" {
		return phase
	}

	// 规则 6:进场(一般 —— 靠近台站,下降或平飞)
	if phase := tt.ruleArrival(derived); phase != "" {
		return phase
	}

	// 规则 7:未知(在空中但不匹配任何模式时的兜底)
	return "UNK"
}

// ─── 各阶段规则 ───────────────────────────────────────────────────

// ruleGround 对地面阶段的飞行器进行分类。
//
// 条件:
//   - TAX:平均地速在滑行范围内(默认 1–50 kts)
//   - TAX(保留):上一阶段为 TAX 且飞行器短暂停下
//   - NEW:其他情况(停场、静止、无先前阶段)
func (tt *TrajectoryTracker) ruleGround(aircraft *Aircraft, currentPhase *PhaseChange, d *DerivedState) string {
	cfg := tt.phasesConfig

	// 滑行:地速在滑行范围内
	if d.GSMean >= float64(cfg.TaxiingMinSpeedKts) && d.GSMean <= float64(cfg.TaxiingMaxSpeedKts) {
		return "TAX"
	}

	// 飞行器短暂停下时保留 TAX 阶段(防止抖动)
	if currentPhase != nil && currentPhase.Phase == "TAX" {
		return "TAX"
	}

	return "NEW"
}

// ruleCruise 检测巡航阶段(航路)。
//
// 条件:平均高度在巡航高度阈值之上(默认 FL180)。
//
// 无平飞要求。FL180 是 A 类空域的下边界 —— 在其之上,无论飞行器
// 仍在爬升至指定高度、平飞巡航,还是开始初始下降,都属于航路阶段。
// DEP 覆盖 FL180 以下的终端区爬升;一旦飞行器进入 A 类空域,则由
// CRZ 接管。这消除了那种因飞行器穿越 FL180 时尚未平飞,而被误判为
// UNK 的间隙。
func (tt *TrajectoryTracker) ruleCruise(d *DerivedState) string {
	if d.AltMean >= float64(tt.phasesConfig.CruiseAltitudeFt) {
		return "CRZ"
	}
	return ""
}

// ruleApproach 检测处于落地最后进近的飞行器。
//
// 条件(全部必须为真):
//   - 低于 approach_max_altitude_ft(默认 5000 英尺)
//   - 没有在爬升(下降或平飞 —— 覆盖在下滑道截获前的航向道截获)
//   - 向台站靠近(距离在减小)
//   - 与某条跑道中线对齐(在中线容差 + 最大距离内)
//   - 朝机场方向飞(方位检查)
//
// 下降要求被刻意放宽为"不爬升",而不是"必须下降"。
// 在真实运行中,飞行器只要建立在航向道上就是进近,即便此时尚未
// 截获下滑道,可能仍处于平飞。要求 30 秒已建立的下降趋势
// (IsDescending)会让 APP 在飞行器转入五边后被推迟 30+ 秒检测到
// —— 这对 ATC 不可接受。
func (tt *TrajectoryTracker) ruleApproach(aircraft *Aircraft, d *DerivedState) string {
	cfg := tt.phasesConfig
	adsb := aircraft.ADSB
	lat, lon, hasPosition := adsb.Position()
	if !hasPosition {
		return ""
	}

	// 必须低于进近高度上限
	if d.AltMean > float64(cfg.ApproachMaxAltitudeFt) {
		return ""
	}

	// 不能在爬升 —— 下降或平飞都是合法的进近状态。
	// 在下滑道截获前已建立航向道的飞行器是平飞,这仍然属于"进近中"。
	if d.IsClimbing {
		return ""
	}

	// 必须正在向台站靠近
	if !d.IsApproachingStation {
		return ""
	}

	// 用最新位置检查跑道对齐
	runwayInfo := DetectRunwayApproach(lat, lon, NumberOrZero(adsb.Track), d.AltMean, tt.runwayData, *cfg)
	if runwayInfo == nil || !runwayInfo.OnApproach {
		return ""
	}

	// 按活跃跑道过滤 —— 抑制朝非活跃跑道的进近
	// (例如基线转弯期间的垂直交叉跑道)。在启动宽限期(无数据)时,
	// IsActiveRunway 对所有跑道返回 true。
	if tt.runwayTracker != nil && !tt.runwayTracker.IsActiveRunway(runwayInfo.RunwayID) {
		tt.logger.Debug("已拒绝进近(跑道未活跃)",
			logger.String("hex", aircraft.Hex),
			logger.String("runway", runwayInfo.RunwayID),
		)
		return ""
	}

	// 验证航向是否朝向机场(方位差 <= 90°)
	bearingToStation := CalculateBearing(lat, lon, tt.stationLat, tt.stationLon)
	headingDiff := math.Abs(NumberOrZero(adsb.Track) - bearingToStation)
	if headingDiff > 180 {
		headingDiff = 360 - headingDiff
	}
	if headingDiff > 90 {
		return ""
	}

	// 为使用中跑道检测记录证据
	if tt.runwayTracker != nil {
		tt.runwayTracker.RecordEvent(runwayInfo.RunwayID, RunwayEventApproach, aircraft.Hex)
	}

	return "APP"
}

// ruleClimb 检测从本机场初始爬升 —— 即 ruleApproach 在离场侧的对应。
// APP 在入场侧要求跑道对齐,CLB 则要求最近起飞的证据加上初始爬升
// 的指标:跑道离场航向、加速,或非常接近机场。
//
// 进入 CLB 的两条路径:
//
// 路径 A(观测到的起飞):数据库中有近期 T/O 记录 + 正在爬升 +
// 在跑道航向上 + 低于进近上限 + 尚未转弯偏离 SID。
//
// 路径 B(推断的起飞):没有 T/O 记录,但飞行器在低高度靠近机场处
// 沿跑道航向爬升出现。在 ADS-B 覆盖断断续续的情况下,飞行器
// 往往会在约 1000 英尺、已沿跑道中线爬升时首次出现 —— 这是显而
// 易见的起飞,只是我们在地面阶段错过了。
//
// 当下列任一发生时 CLB 结束:
//   - 飞行器偏离跑道航向(SID 转弯 → DEP)
//   - 飞行器爬升至进近高度上限以上(通常 5000 英尺)
//   - 飞行器离开机场区域
func (tt *TrajectoryTracker) ruleClimb(aircraft *Aircraft, d *DerivedState, takeoffTime *time.Time) string {
	cfg := tt.phasesConfig

	// 必须在爬升
	if !d.IsClimbing {
		return ""
	}

	// 必须低于巡航高度
	if d.AltMean >= float64(cfg.CruiseAltitudeFt) {
		return ""
	}

	// CLB 是从跑道初始爬升的短暂阶段,持续到首次 SID 转弯,或者
	// 飞行器穿过进近高度上限为止。超过该高度后,飞行器已远超
	// 初始爬升,应归入 DEP。
	if d.AltMean > float64(cfg.ApproachMaxAltitudeFt) {
		return ""
	}

	// 一旦飞行器开始转弯,初始爬升就结束了。
	// SID 转弯通常在起飞后很快发生 —— 这是 CLB 应转换为 DEP 的
	// 强信号。
	if d.IsTurning {
		return ""
	}

	// 检查跑道离场航向(两条路径都使用)
	adsb := aircraft.ADSB
	var departureInfo *RunwayDepartureInfo
	if adsb != nil {
		lat, lon, hasPosition := adsb.Position()
		if hasPosition {
			departureInfo = DetectRunwayDeparture(
				lat, lon, NumberOrZero(adsb.Track),
				tt.runwayData, tt.stationLat, tt.stationLon, *cfg,
			)
		}
	}

	// ── 路径 A:观测到的起飞 ──
	if tt.hasRecentTakeoff(aircraft, takeoffTime) {
		// (a) 在跑道离场航向上
		if departureInfo != nil && departureInfo.OnDeparture {
			if tt.runwayTracker != nil {
				tt.runwayTracker.RecordEvent(departureInfo.RunwayID, RunwayEventClimb, aircraft.Hex)
			}
			return "CLB"
		}

		// (b) 仍在机场附近且高度较低 —— 还没有进入 SID
		if d.DistToStationNM <= cfg.AirportRangeNM {
			return "CLB"
		}
	}

	// ── 路径 B:推断的起飞(出现时已在空中,无 T/O 记录) ──
	// 飞行器沿跑道离场航向、低高度、靠近机场爬升。
	// 在 ADS-B 覆盖断续的情况下,这是最常见的 CLB 场景。
	if departureInfo != nil && departureInfo.OnDeparture &&
		d.AltMean <= float64(cfg.DepartureAltitudeFt) &&
		d.DistToStationNM <= cfg.AirportRangeNM {
		if tt.runwayTracker != nil {
			tt.runwayTracker.RecordEvent(departureInfo.RunwayID, RunwayEventClimb, aircraft.Hex)
		}
		return "CLB"
	}

	return ""
}

// ruleDeparture 检测处于一般离场爬升的飞行器 —— 即在巡航高度
// 以下爬升且远离台站飞行。这是 ruleArrival 在离场侧的对应:
// ARR 捕获向台站下降的入场流量,DEP 捕获向外爬升的离场流量。
//
// 条件(全部必须为真):
//   - 在爬升(轨迹趋势)
//   - 在巡航高度以下
//   - 正在离开台站(距离在增大)
//
// DEP 覆盖:本机场起飞后的离场(CLB → DEP 转换)、来自邻近机场
// 的过境流量、错过 T/O 的离场,以及其他任何向外爬升的飞行器。
// CLB(优先级更高)占据初始爬升;之后到 FL180 之间由 DEP 处理。
func (tt *TrajectoryTracker) ruleDeparture(d *DerivedState) string {
	// 必须在爬升
	if !d.IsClimbing {
		return ""
	}

	// 必须低于巡航高度
	if d.AltMean >= float64(tt.phasesConfig.CruiseAltitudeFt) {
		return ""
	}

	// 必须正在离开台站
	if d.DistTrendNMPerSec <= 0.001 {
		return ""
	}

	return "DEP"
}

// ruleArrival 检测飞向台站机场的飞行器。
// 这并不是兜底规则 —— 它要求飞行器确实在向台站靠近。
//
// 条件(全部必须为真):
//   - 在向台站靠近(轨迹窗口内距离在减小)
//   - 在巡航高度以下
//   - 下降或平飞(不爬升 —— 一架在靠近台站的同时爬升的飞行器很可能
//     是失误进近/复飞,应被判为 UNK 或 DEP)
func (tt *TrajectoryTracker) ruleArrival(d *DerivedState) string {
	cruiseAlt := float64(tt.phasesConfig.CruiseAltitudeFt)

	// 必须正在向台站靠近
	if !d.IsApproachingStation {
		return ""
	}

	// 必须低于巡航高度
	if d.AltMean >= cruiseAlt {
		return ""
	}

	// 必须在下降或平飞(不爬升)
	if d.IsClimbing {
		return ""
	}

	return "ARR"
}

// ─── Simplified Heuristics for Limited Data ───────────────────────────────────

// singlePointPhase provides a best-effort phase classification when only a single
// data point is available (aircraft just appeared). Uses simple altitude/speed
// thresholds without trajectory analysis.
func (tt *TrajectoryTracker) singlePointPhase(aircraft *Aircraft) string {
	if aircraft.ADSB == nil {
		return "NEW"
	}
	if aircraft.OnGround {
		adsb := aircraft.ADSB
		cfg := tt.phasesConfig
		groundSpeed := NumberOrZero(adsb.GS)
		if groundSpeed >= float64(cfg.TaxiingMinSpeedKts) && groundSpeed <= float64(cfg.TaxiingMaxSpeedKts) {
			return "TAX"
		}
		return "NEW"
	}

	// Airborne with just one data point — use altitude as primary discriminator.
	//
	// First observations are often weak Mode S contacts with incomplete data:
	// Track=0 (no heading), AltBaro=0 (no altitude), Lat/Lon=0 (no position).
	// We must guard against treating these zero values as real measurements.
	adsb := aircraft.ADSB
	alt := adsb.AltBaro.Float64()
	cfg := tt.phasesConfig
	cruiseAlt := float64(cfg.CruiseAltitudeFt)
	hasPosition := adsb.HasPosition()
	hasTrack := NumberOrZero(adsb.Track) != 0 || NumberOrZero(adsb.MagHeading) != 0 || NumberOrZero(adsb.TrueHeading) != 0
	hasAltitude := alt > 0

	if hasAltitude && alt >= cruiseAlt {
		return "CRZ"
	}

	// Low altitude, near airport, on runway departure heading → likely CLB.
	// Requires real position, heading, and altitude data (not Mode S zeros).
	// When the active runway is known, also accept aircraft on its heading
	// even without full runway geometry match — very strong signal.
	if hasPosition && hasTrack && hasAltitude && alt <= float64(cfg.DepartureAltitudeFt) {
		lat, lon, _ := adsb.Position()
		track := NumberOrZero(adsb.Track)
		if track == 0 && NumberOrZero(adsb.MagHeading) != 0 {
			track = NumberOrZero(adsb.MagHeading)
		}
		distToStation := MetersToNM(Haversine(lat, lon, tt.stationLat, tt.stationLon))
		if distToStation <= cfg.AirportRangeNM {
			departureInfo := DetectRunwayDeparture(
				lat, lon, track,
				tt.runwayData, tt.stationLat, tt.stationLon, *cfg,
			)
			if departureInfo != nil && departureInfo.OnDeparture {
				// Extra confidence: matches the known active runway
				if tt.runwayTracker != nil && tt.runwayTracker.IsActiveRunway(departureInfo.RunwayID) {
					return "CLB"
				}
				// No active runway data yet — still accept if geometry matches
				return "CLB"
			}
		}
	}

	// Below cruise, single data point — we can't determine trend, so UNK
	return "UNK"
}

// fewPointsAirbornePhase provides phase classification when we have 2-4 data points.
// This is a transitional period — we have some trend information but not enough for
// full trajectory analysis. We use a simplified version of the rules.
func (tt *TrajectoryTracker) fewPointsAirbornePhase(
	aircraft *Aircraft,
	d *DerivedState,
	currentPhase *PhaseChange,
	takeoffTime *time.Time,
) string {
	cruiseAlt := float64(tt.phasesConfig.CruiseAltitudeFt)

	// Cruise is unambiguous even with few points
	if d.AltMean >= cruiseAlt {
		return "CRZ"
	}

	// Recent takeoff from our airport and climbing = initial climb out
	if tt.hasRecentTakeoff(aircraft, takeoffTime) && d.VRMean > 0 {
		return "CLB"
	}

	// Inferred takeoff: climbing on runway heading, low altitude, near airport.
	// With spotty ADS-B coverage, aircraft often first appear already airborne.
	// Use MagHeading as fallback when Track is 0 (common with early Mode S contacts).
	if d.VRMean > 0 && d.AltMean <= float64(tt.phasesConfig.DepartureAltitudeFt) &&
		d.DistToStationNM <= tt.phasesConfig.AirportRangeNM && aircraft.ADSB != nil {
		lat, lon, hasPosition := aircraft.ADSB.Position()
		if !hasPosition {
			return "UNK"
		}
		track := NumberOrZero(aircraft.ADSB.Track)
		if track == 0 && NumberOrZero(aircraft.ADSB.MagHeading) != 0 {
			track = NumberOrZero(aircraft.ADSB.MagHeading)
		}
		if track != 0 {
			departureInfo := DetectRunwayDeparture(
				lat, lon, track,
				tt.runwayData, tt.stationLat, tt.stationLon, *tt.phasesConfig,
			)
			if departureInfo != nil && departureInfo.OnDeparture {
				return "CLB"
			}
		}
	}

	// Climbing and moving away from station = general departure
	if d.VRMean > 0 && d.DistTrendNMPerSec > 0.001 {
		return "DEP"
	}

	// Approaching station and descending
	if d.IsApproachingStation && d.VRMean < 0 && d.AltMean < cruiseAlt {
		return "ARR"
	}

	return "UNK"
}

// hasRecentTakeoff determines if an aircraft has taken off from our airport recently.
// Only returns true if we actually observed the takeoff (T/O phase record in DB).
// No proximity heuristic — aircraft appearing mid-flight near the airport are NOT
// treated as recent takeoffs.
func (tt *TrajectoryTracker) hasRecentTakeoff(aircraft *Aircraft, takeoffTime *time.Time) bool {
	cfg := tt.phasesConfig
	timeoutDuration := time.Duration(cfg.RecentTakeoffTimeoutMinutes) * time.Minute

	// Only trust actual observed takeoff records from the database
	if takeoffTime != nil && time.Since(*takeoffTime) <= timeoutDuration {
		return true
	}

	return false
}

// ─── Signal-Lost Landing Enhancement ──────────────────────────────────────────

// WasDescendingTowardStation checks the trajectory history to determine if an
// aircraft was on a descending trajectory toward the station before signal was lost.
// Returns true with high confidence when the medium-window altitude trend shows
// steady descent and the aircraft was closing on the station.
//
// This is used by detectSignalLostLandings() to boost confidence in auto-landing
// inference when an aircraft disappears near the airport.
func (tt *TrajectoryTracker) WasDescendingTowardStation(hex string) bool {
	tt.mu.RLock()
	at, ok := tt.aircraft[hex]
	tt.mu.RUnlock()
	if !ok || at.Count < 3 {
		return false
	}

	// Ensure derived state is fresh
	if at.DirtyDerived {
		tt.computeDerivedState(at)
		at.DirtyDerived = false
	}

	d := &at.Derived

	// Check: was descending AND approaching station
	return d.IsDescending && d.IsApproachingStation && d.AltTrendFPM < -100

	// Note: We use a gentler threshold (-100 fpm) than the normal descent threshold
	// (-200 fpm) because near landing the descent rate may be quite shallow.
}

// ─── Logging ──────────────────────────────────────────────────────────────────

// LogDerivedState logs the key derived state fields for debugging.
func (tt *TrajectoryTracker) LogDerivedState(hex string) {
	d := tt.EnsureDerived(hex)
	if d == nil {
		return
	}
	tt.logger.Debug("Trajectory derived state",
		logger.String("hex", hex),
		logger.Int("valid_points", d.ValidPointCount),
		logger.Float64("alt_mean", d.AltMean),
		logger.Float64("alt_trend_fpm", d.AltTrendFPM),
		logger.Float64("gs_mean", d.GSMean),
		logger.Float64("gs_trend", d.GSTrendKtsPerSec),
		logger.Float64("vr_mean", d.VRMean),
		logger.Float64("dist_nm", d.DistToStationNM),
		logger.Float64("dist_trend", d.DistTrendNMPerSec),
		logger.Bool("descending", d.IsDescending),
		logger.Bool("climbing", d.IsClimbing),
		logger.Bool("level", d.IsLevel),
		logger.Bool("approaching", d.IsApproachingStation),
		logger.Bool("turning", d.IsTurning),
	)
}
