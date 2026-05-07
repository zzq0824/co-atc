package adsb

import (
	"github.com/yegors/co-atc/pkg/logger"
)

// ChangeDetector 跟踪轮询周期之间的飞行器变化
type ChangeDetector struct {
	previousAircraft map[string]*Aircraft
	logger           *logger.Logger
}

// NewChangeDetector 创建一个新的变化检测器
func NewChangeDetector(logger *logger.Logger) *ChangeDetector {
	return &ChangeDetector{
		previousAircraft: make(map[string]*Aircraft),
		logger:           logger.Named("change-detector"),
	}
}

// AircraftChange 表示飞行器数据的变化
type AircraftChange struct {
	Type     string                 // "added","updated","removed"
	Aircraft *Aircraft              // "added" 的完整对象,其他为 nil
	Hex      string                 // 飞行器十六进制代码
	Delta    map[string]interface{} // 仅变化的字段(用于 "updated")
}

// DetectChanges 比较当前飞行器数据与之前的数据并返回变化
func (cd *ChangeDetector) DetectChanges(currentAircraft []*Aircraft) []AircraftChange {
	changes := []AircraftChange{}
	currentMap := make(map[string]*Aircraft)

	// 构建当前飞行器映射
	for _, aircraft := range currentAircraft {
		currentMap[aircraft.Hex] = aircraft
	}

	// 检测新增和更新的飞行器
	for hex, current := range currentMap {
		if previous, exists := cd.previousAircraft[hex]; exists {
			// 计算增量 - 仅在有实际变化时返回非空
			delta := cd.computeDelta(previous, current)
			if len(delta) > 0 {
				changes = append(changes, AircraftChange{
					Type:     "updated",
					Aircraft: current,
					Hex:      hex,
					Delta:    delta,
				})
			}
		} else {
			// 新飞行器 - 发送完整对象
			changes = append(changes, AircraftChange{
				Type:     "added",
				Aircraft: current,
				Hex:      hex,
			})
		}
	}

	// 检测移除的飞行器
	for hex := range cd.previousAircraft {
		if _, exists := currentMap[hex]; !exists {
			changes = append(changes, AircraftChange{
				Type: "removed",
				Hex:  hex,
			})
		}
	}

	// 更新先前状态
	cd.previousAircraft = currentMap
	return changes
}

// computeDelta 仅返回先前与当前飞行器之间发生变化的字段
func (cd *ChangeDetector) computeDelta(previous, current *Aircraft) map[string]interface{} {
	delta := make(map[string]interface{})

	// 比较 ADSB 数据
	if previous.ADSB != nil && current.ADSB != nil {
		// 位置
		if !floatPtrEqual(previous.ADSB.Lat, current.ADSB.Lat) {
			delta["lat"] = current.ADSB.Lat
		}
		if !floatPtrEqual(previous.ADSB.Lon, current.ADSB.Lon) {
			delta["lon"] = current.ADSB.Lon
		}

		// 高度
		if previous.ADSB.AltBaro != current.ADSB.AltBaro {
			delta["alt_baro"] = current.ADSB.AltBaro
		}

		// 航迹
		if previous.ADSB.Track != current.ADSB.Track {
			delta["track"] = current.ADSB.Track
		}

		// 地速
		if !floatPtrEqual(previous.ADSB.GS, current.ADSB.GS) {
			delta["gs"] = current.ADSB.GS
		}

		// 真空速
		if !floatPtrEqual(previous.ADSB.TAS, current.ADSB.TAS) {
			delta["tas"] = current.ADSB.TAS
		}

		// 气压速率
		if !floatPtrEqual(previous.ADSB.BaroRate, current.ADSB.BaroRate) {
			delta["baro_rate"] = current.ADSB.BaroRate
		}

		// 磁航向
		if previous.ADSB.MagHeading != current.ADSB.MagHeading {
			delta["mag_heading"] = current.ADSB.MagHeading
		}

		// 真航向
		if previous.ADSB.TrueHeading != current.ADSB.TrueHeading {
			delta["true_heading"] = current.ADSB.TrueHeading
		}

		// ATC 派生指标
		if !atcDerivedEqual(previous.ADSB.ATCDerived, current.ADSB.ATCDerived) {
			delta["atc_derived"] = current.ADSB.ATCDerived
		}
	} else if (previous.ADSB == nil) != (current.ADSB == nil) {
		// ADSB 数据出现或消失 - 发送完整 ADSB 对象
		if current.ADSB != nil {
			delta["adsb"] = current.ADSB
		} else {
			delta["adsb"] = nil
		}
	}

	// 比较基本飞行器属性
	if previous.Flight != current.Flight {
		delta["flight"] = current.Flight
	}

	if previous.Status != current.Status {
		delta["status"] = current.Status
	}

	if previous.OnGround != current.OnGround {
		delta["on_ground"] = current.OnGround
	}

	// 比较阶段数据(优化 - 不使用反射)
	if !phaseDataEqual(previous.Phase, current.Phase) {
		delta["phase"] = current.Phase
	}

	// 比较距离
	if (previous.Distance == nil) != (current.Distance == nil) ||
		(previous.Distance != nil && current.Distance != nil && *previous.Distance != *current.Distance) {
		delta["distance"] = current.Distance
	}

	// 比较 BSDB 数据(BaseStation.sqb 增强)
	if !bsdbDataEqual(previous.BSDB, current.BSDB) {
		delta["bsdb"] = current.BSDB
	}

	// last_seen 来自 ADS-B 观测(服务器端 LastSeen)。

	return delta
}

// phaseDataEqual 不使用反射比较两个 PhaseData 结构
// 仅比较当前阶段,因为这是变化检测的关键
func phaseDataEqual(a, b *PhaseData) bool {
	// 都为 nil = 相等
	if a == nil && b == nil {
		return true
	}
	// 一个为 nil,一个不为 nil = 不相等
	if a == nil || b == nil {
		return false
	}
	// 比较当前阶段数组的长度
	if len(a.Current) != len(b.Current) {
		return false
	}
	// 如果两者都有当前阶段,比较阶段字符串和时间戳
	if len(a.Current) > 0 && len(b.Current) > 0 {
		if a.Current[0].Phase != b.Current[0].Phase {
			return false
		}
		if !a.Current[0].Timestamp.Equal(b.Current[0].Timestamp) {
			return false
		}
	}
	return true
}

// bsdbDataEqual 不使用反射比较两个 BSDBData 结构
// 如果两者相等(包括都为 nil)则返回 true
func bsdbDataEqual(a, b *BSDBData) bool {
	// 都为 nil = 相等
	if a == nil && b == nil {
		return true
	}
	// 一个为 nil,一个不为 nil = 不相等
	if a == nil || b == nil {
		return false
	}
	// 比较所有字段
	return a.Registration == b.Registration &&
		a.ICAOTypeCode == b.ICAOTypeCode &&
		a.OperatorFlagCode == b.OperatorFlagCode &&
		a.Manufacturer == b.Manufacturer &&
		a.Type == b.Type &&
		a.RegisteredOwners == b.RegisteredOwners
}
