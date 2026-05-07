package adsb

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// ─── 使用中跑道检测 ─────────────────────────────────────────────────
//
// RunwayInUseTracker 通过观察飞行器活动(进近、着陆、离场)来判定
// 当前活跃的跑道末端。它维护带权重证据事件的滚动时间窗口,
// 并基于这些事件生成跑道末端的概率分布。
//
// 主要使用者是 ruleApproach:当我们有足够数据来确定活跃跑道时,
// 对非活跃跑道(例如基线转弯时的垂直交叉跑道)的进近会被抑制。
//
// 线程安全:数据获取 goroutine 在同一 goroutine 中调用 RecordEvent 和
// IsActiveRunway,但 API 处理器也可能查询此 tracker。

// RunwayEventType 按来源分类证据事件。
type RunwayEventType int

const (
	RunwayEventApproach RunwayEventType = iota // 飞行器在此跑道末端进入 APP
	RunwayEventLanding                         // 飞行器接地(T/D)
	RunwayEventClimb                           // 飞行器在此跑道末端爬升离场(CLB)
)

func (t RunwayEventType) String() string {
	switch t {
	case RunwayEventApproach:
		return "approach"
	case RunwayEventLanding:
		return "landing"
	case RunwayEventClimb:
		return "climb"
	default:
		return "unknown"
	}
}

// RunwayEvent 记录一条跑道末端在用的证据。
type RunwayEvent struct {
	RunwayEnd string          // "05-23/05" 格式(与 RunwayApproachInfo.RunwayID 匹配)
	Type      RunwayEventType
	Hex       string // 触发此事件的飞行器
	Timestamp time.Time
}

// RunwayScore 保存某条跑道末端的计算分数和概率。
type RunwayScore struct {
	RunwayEnd   string  `json:"runway_end"`
	Score       float64 `json:"score"`
	Probability float64 `json:"probability"` // 归一化 0-1
	EventCount  int     `json:"event_count"`
}

// RunwayInUseTracker 维护跑道使用证据的滚动窗口,
// 并计算当前哪些跑道末端处于活跃状态。
type RunwayInUseTracker struct {
	mu             sync.RWMutex
	events         []RunwayEvent
	windowDuration time.Duration
	weights        [3]float64 // 按 RunwayEventType 索引
	decayRate      float64    // 每分钟指数衰减
	logger         *logger.Logger

	// 上次重算后的缓存状态
	scores        []RunwayScore // 按分数降序排序
	activeSet     map[string]bool
	lastActiveEnd string    // 用于变更检测
	lastLogTime   time.Time // 节流周期性 Info 日志

	// 持久化状态 —— 当窗口内所有事件都已过期时仍保留,
	// 这样在低流量时段 UI 显示的是最后已知的活跃跑道而不是 N/A。
	everHadData bool

	// 跑道数据中所有已知的跑道末端 ID,用于并行跑道检测。
	// 当并行对中的一条跑道活跃时,另一条会自动加入活跃集合
	// (例如 06L 活跃 → 06R 也活跃)。
	knownRunwayEnds []string
}

const (
	// 跑道被视为"活跃"的最低概率(相对值)。
	// 用于处理并行跑道运行的场景(例如两条并行跑道各自约 40%)。
	activeMinProbability = 0.15

	// 周期性分数日志之间的最小时间间隔。
	logThrottleInterval = 60 * time.Second
)

// NewRunwayInUseTracker 使用给定的评分参数创建一个 tracker。
func NewRunwayInUseTracker(
	windowMinutes int,
	approachWeight, landingWeight, climbWeight, decayRate float64,
	log *logger.Logger,
) *RunwayInUseTracker {
	rt := &RunwayInUseTracker{
		windowDuration: time.Duration(windowMinutes) * time.Minute,
		weights:        [3]float64{approachWeight, landingWeight, climbWeight},
		decayRate:      decayRate,
		logger:         log.Named("runway-use"),
		activeSet:      make(map[string]bool),
	}
	rt.logger.Info("使用中跑道 tracker 已启动",
		logger.Int("window_minutes", windowMinutes),
		logger.Float64("approach_weight", approachWeight),
		logger.Float64("landing_weight", landingWeight),
		logger.Float64("climb_weight", climbWeight),
		logger.Float64("decay_rate", decayRate),
	)
	return rt
}

// RecordEvent 记录跑道使用证据并重新计算分数。
func (rt *RunwayInUseTracker) RecordEvent(runwayID string, eventType RunwayEventType, hex string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now().UTC()
	rt.events = append(rt.events, RunwayEvent{
		RunwayEnd: runwayID,
		Type:      eventType,
		Hex:       hex,
		Timestamp: now,
	})

	rt.logger.Debug("已记录跑道事件",
		logger.String("runway", runwayID),
		logger.String("type", eventType.String()),
		logger.String("hex", hex),
	)

	rt.recompute(now)
}

// IsActiveRunway 检查某个跑道末端是否在当前活跃集合中。
// 在启动宽限期(从未有过数据)返回 true,以便在观察到足够流量之前
// 接受所有跑道。一旦有了数据,使用活跃集合(在非活跃时段也会保留)。
func (rt *RunwayInUseTracker) IsActiveRunway(runwayID string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// 启动宽限:从未有过任何数据 → 接受所有跑道
	if !rt.everHadData {
		return true
	}
	return rt.activeSet[runwayID]
}

// HasData 当前窗口中存在任何事件时返回 true。
func (rt *RunwayInUseTracker) HasData() bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.events) > 0
}

// GetTopScores 返回前 N 个跑道分数,按分数降序排序。
func (rt *RunwayInUseTracker) GetTopScores(n int) []RunwayScore {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	if n > len(rt.scores) {
		n = len(rt.scores)
	}
	result := make([]RunwayScore, n)
	copy(result, rt.scores[:n])
	return result
}

// recompute 清理过期事件,重新计算按时间衰减的分数,并更新
// 活跃集合。必须在持有 rt.mu 的写锁时调用。
func (rt *RunwayInUseTracker) recompute(now time.Time) {
	// ── 清理窗口外的事件 ──
	cutoff := now.Add(-rt.windowDuration)
	writeIdx := 0
	for _, e := range rt.events {
		if e.Timestamp.After(cutoff) {
			rt.events[writeIdx] = e
			writeIdx++
		}
	}
	rt.events = rt.events[:writeIdx]

	// ── 计算每个跑道末端的时间衰减加权分数 ──
	type accumulator struct {
		score float64
		count int
	}
	scoreMap := make(map[string]*accumulator)

	for _, e := range rt.events {
		minutesAgo := now.Sub(e.Timestamp).Minutes()
		weight := rt.weights[e.Type] * math.Pow(rt.decayRate, minutesAgo)

		acc, ok := scoreMap[e.RunwayEnd]
		if !ok {
			acc = &accumulator{}
			scoreMap[e.RunwayEnd] = acc
		}
		acc.score += weight
		acc.count++
	}

	// ── 构建排序后的分数列表 ──
	scores := make([]RunwayScore, 0, len(scoreMap))
	totalScore := 0.0
	for end, acc := range scoreMap {
		totalScore += acc.score
		scores = append(scores, RunwayScore{
			RunwayEnd:  end,
			Score:      acc.score,
			EventCount: acc.count,
		})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	// ── 归一化为概率 ──
	if totalScore > 0 {
		for i := range scores {
			scores[i].Probability = scores[i].Score / totalScore
		}
	}

	// 如果有实时数据,更新分数并标记我们已经看到数据。
	// 如果所有事件都已过期(低流量),保留最后已知的 scores/activeSet
	// 以便 UI 显示最后的活跃跑道而不是 N/A。
	if len(scores) > 0 {
		rt.scores = scores
		rt.everHadData = true

		// ── 构建活跃集合(概率 >= 阈值)──
		rt.activeSet = make(map[string]bool, len(scores))
		for _, s := range scores {
			if s.Probability >= activeMinProbability {
				rt.activeSet[s.RunwayEnd] = true
			}
		}

		// ── 为并行跑道做扩展 ──
		// 如果 06L 活跃,则 06R 也自动包含(反之亦然)。
		// 这打破了第二条并行跑道因 IsActiveRunway 拒绝而永远
		// 无法获得 APP 事件的鸡生蛋蛋生鸡问题。
		rt.expandActiveSetForParallels()
	}
	// 否则:保持原有的 rt.scores 和 rt.activeSet 不变

	// ── 日志 ──
	newActiveEnd := ""
	if len(rt.scores) > 0 {
		newActiveEnd = rt.scores[0].RunwayEnd
	}

	// 活跃跑道变化时记录
	if newActiveEnd != rt.lastActiveEnd && newActiveEnd != "" {
		if rt.lastActiveEnd != "" {
			rt.logger.Info("活跃跑道已变更",
				logger.String("previous", formatRunwayEnd(rt.lastActiveEnd)),
				logger.String("current", formatRunwayEnd(newActiveEnd)),
				logger.String("scores", formatScores(rt.scores)),
			)
		} else {
			rt.logger.Info("已检测到初始活跃跑道",
				logger.String("runway", formatRunwayEnd(newActiveEnd)),
				logger.String("scores", formatScores(rt.scores)),
			)
		}
		rt.lastActiveEnd = newActiveEnd
	}

	// 周期性分数摘要(已节流)
	if now.Sub(rt.lastLogTime) >= logThrottleInterval {
		if len(rt.scores) > 0 {
			n := 3
			if n > len(rt.scores) {
				n = len(rt.scores)
			}
			rt.logger.Info("使用中跑道",
				logger.String("scores", formatScores(rt.scores[:n])),
				logger.Int("total_events", len(rt.events)),
			)
		}
		rt.lastLogTime = now
	}
}

// SetRunwayData 向 tracker 提供所有已知跑道末端 ID,用于并行跑道
// 检测。当并行对中的一条跑道(例如 06L)活跃时,tracker 会自动
// 把并行的另一条(06R)加入活跃集合。
func (rt *RunwayInUseTracker) SetRunwayData(runways RunwayData) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	var ends []string
	for pairKey, thresholds := range runways.RunwayThresholds {
		for endID := range thresholds {
			ends = append(ends, pairKey+"/"+endID)
		}
	}
	rt.knownRunwayEnds = ends

	if len(ends) > 0 {
		rt.logger.Info("已加载用于并行检测的跑道数据",
			logger.Int("runway_ends", len(ends)),
		)
	}
}

// runwayEndIdent 从完整的跑道 ID 中提取末端标识符。
// "06L-24R/06L" → "06L", "05-23/05" → "05"
func runwayEndIdent(runwayID string) string {
	parts := strings.SplitN(runwayID, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return runwayID
}

// runwayBaseNumber 从跑道末端标识符中去除 L/R/C 后缀,得到
// 数字航向。"06L" → "06", "24R" → "24", "33" → "33"
func runwayBaseNumber(endIdent string) string {
	if len(endIdent) == 0 {
		return ""
	}
	last := endIdent[len(endIdent)-1]
	if last == 'L' || last == 'R' || last == 'C' {
		return endIdent[:len(endIdent)-1]
	}
	return endIdent
}

// expandActiveSetForParallels 把并行跑道末端加入活跃集合。
// 如果 "06L-24R/06L" 活跃,则把 "06R-24L/06R" 也标记为活跃
// (如果它存在于已知跑道数据中)。这打破了第二条并行跑道因
// IsActiveRunway 拒绝而永远无法累积进近事件的鸡生蛋蛋生鸡问题。
func (rt *RunwayInUseTracker) expandActiveSetForParallels() {
	if len(rt.knownRunwayEnds) == 0 {
		return
	}

	// 收集当前所有活跃跑道末端的基础编号
	activeBases := make(map[string]bool)
	for activeID := range rt.activeSet {
		base := runwayBaseNumber(runwayEndIdent(activeID))
		if base != "" {
			activeBases[base] = true
		}
	}

	// 添加任何基础编号与活跃基础匹配的已知跑道末端
	for _, knownID := range rt.knownRunwayEnds {
		if rt.activeSet[knownID] {
			continue // 已经活跃
		}
		base := runwayBaseNumber(runwayEndIdent(knownID))
		if base != "" && activeBases[base] {
			rt.activeSet[knownID] = true
		}
	}
}

// formatRunwayEnd 从 "05-23/05" 格式中提取易读的跑道名称。
// 返回末端标识符(例如 "05"),并附上跑道对作为上下文。
func formatRunwayEnd(runwayID string) string {
	// RunwayID 格式:"pairKey/endIdent",例如 "05-23/05"
	parts := strings.SplitN(runwayID, "/", 2)
	if len(parts) == 2 {
		return fmt.Sprintf("RWY %s (%s)", parts[1], parts[0])
	}
	return runwayID
}

// formatScores 生成简洁、便于日志的跑道分数字符串。
func formatScores(scores []RunwayScore) string {
	var b strings.Builder
	for i, s := range scores {
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "%s: %.1f (%.0f%%, %d events)",
			formatRunwayEnd(s.RunwayEnd), s.Score, s.Probability*100, s.EventCount)
	}
	return b.String()
}
