package simulation

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/pkg/logger"
)

const (
	MaxSimulatedAircraft = 10 // 模拟飞行器数量的硬编码上限
)

// SimulatedAircraft 表示一架模拟飞行器及其当前状态
type SimulatedAircraft struct {
	Hex                string    `json:"hex"`
	Flight             string    `json:"flight"`
	AircraftType       string    `json:"aircraft_type"`
	CurrentLat         float64   `json:"current_lat"`
	CurrentLon         float64   `json:"current_lon"`
	CurrentAltitude    float64   `json:"current_altitude"`
	TargetHeading      float64   `json:"target_heading"`
	TargetSpeed        float64   `json:"target_speed"`
	TargetVerticalRate float64   `json:"target_vertical_rate"`
	LastUpdate         time.Time `json:"last_update"`
	CreatedAt          time.Time `json:"created_at"`
}

// Service 管理所有模拟飞行器
type Service struct {
	aircraft map[string]*SimulatedAircraft
	mutex    sync.RWMutex
	logger   *logger.Logger
}

// NewService 创建一个新的仿真服务
func NewService(logger *logger.Logger) *Service {
	return &Service{
		aircraft: make(map[string]*SimulatedAircraft),
		logger:   logger.Named("simulation"),
	}
}

// CreateAircraft 创建一架新的模拟飞行器
func (s *Service) CreateAircraft(lat, lon, altitude, heading, speed, verticalRate float64) (*SimulatedAircraft, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 检查是否达到上限
	if len(s.aircraft) >= MaxSimulatedAircraft {
		return nil, fmt.Errorf("已达到模拟飞行器数量上限 (%d)", MaxSimulatedAircraft)
	}

	// 生成唯一标识
	hex := s.generateUniqueHex()
	flight := s.generateFlightNumber()

	aircraft := &SimulatedAircraft{
		Hex:                hex,
		Flight:             flight,
		AircraftType:       "SIM",
		CurrentLat:         lat,
		CurrentLon:         lon,
		CurrentAltitude:    altitude,
		TargetHeading:      heading,
		TargetSpeed:        speed,
		TargetVerticalRate: verticalRate,
		LastUpdate:         time.Now().UTC(),
		CreatedAt:          time.Now().UTC(),
	}

	s.aircraft[hex] = aircraft
	s.logger.Info(fmt.Sprintf("已创建模拟飞行器 hex=%s flight=%s lat=%.6f lon=%.6f", hex, flight, lat, lon))

	return aircraft, nil
}

// UpdateControls 更新某架模拟飞行器的控制参数
func (s *Service) UpdateControls(hex string, heading, speed, verticalRate float64) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	aircraft, exists := s.aircraft[hex]
	if !exists {
		return fmt.Errorf("未找到 hex 为 %s 的模拟飞行器", hex)
	}

	aircraft.TargetHeading = heading
	aircraft.TargetSpeed = speed
	aircraft.TargetVerticalRate = verticalRate

	s.logger.Debug(fmt.Sprintf("已更新仿真控制参数 hex=%s heading=%.1f speed=%.1f vs=%.0f", hex, heading, speed, verticalRate))
	return nil
}

// RemoveAircraft 移除一架模拟飞行器
func (s *Service) RemoveAircraft(hex string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if _, exists := s.aircraft[hex]; !exists {
		return fmt.Errorf("未找到 hex 为 %s 的模拟飞行器", hex)
	}

	delete(s.aircraft, hex)
	s.logger.Info(fmt.Sprintf("已移除模拟飞行器 hex=%s", hex))
	return nil
}

// GetAircraft 通过 hex 码获取一架模拟飞行器
func (s *Service) GetAircraft(hex string) (interface{}, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	aircraft, exists := s.aircraft[hex]
	return aircraft, exists
}

// GetAllAircraft 返回所有模拟飞行器
func (s *Service) GetAllAircraft() interface{} {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	result := make([]*SimulatedAircraft, 0, len(s.aircraft))
	for _, aircraft := range s.aircraft {
		result = append(result, aircraft)
	}
	return result
}

// UpdatePositions 根据控制参数更新所有模拟飞行器的位置
func (s *Service) UpdatePositions() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	now := time.Now().UTC()
	for _, aircraft := range s.aircraft {
		deltaTime := now.Sub(aircraft.LastUpdate).Seconds()
		if deltaTime > 0 {
			s.updateAircraftPosition(aircraft, deltaTime)
			aircraft.LastUpdate = now
		}
	}
}

// GenerateADSBData 为所有模拟飞行器生成 ADSB 数据
func (s *Service) GenerateADSBData() []adsb.ADSBTarget {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	targets := make([]adsb.ADSBTarget, 0, len(s.aircraft))
	for _, aircraft := range s.aircraft {
		target := adsb.ADSBTarget{
			Hex:          aircraft.Hex,
			Type:         "sim", // 标记为模拟飞行器
			Flight:       aircraft.Flight,
			AircraftType: aircraft.AircraftType,
			Lat:          adsb.NumberPtr(aircraft.CurrentLat),
			Lon:          adsb.NumberPtr(aircraft.CurrentLon),
			AltBaro:      adsb.FlexibleFloat64(aircraft.CurrentAltitude),
			AltGeom:      adsb.FlexibleFloat64(aircraft.CurrentAltitude),
			TAS:          adsb.NumberPtr(aircraft.TargetSpeed),
			GS:           adsb.NumberPtr(aircraft.TargetSpeed), // 简化处理:假设无风
			Track:        adsb.NumberPtr(aircraft.TargetHeading),
			MagHeading:   adsb.NumberPtr(aircraft.TargetHeading),
			TrueHeading:  adsb.NumberPtr(aircraft.TargetHeading),
			BaroRate:     adsb.NumberPtr(aircraft.TargetVerticalRate),
			GeomRate:     adsb.NumberPtr(aircraft.TargetVerticalRate),
			Seen:         adsb.NumberPtr(0),   // 始终视为最新
			Messages:     adsb.IntPtr(100),    // 伪造消息计数
			RSSI:         adsb.NumberPtr(-20), // 良好的信号强度
		}
		targets = append(targets, target)
	}

	return targets
}

// updateAircraftPosition 通过航位推算更新单架飞行器的位置
func (s *Service) updateAircraftPosition(aircraft *SimulatedAircraft, deltaTime float64) {
	// 将航向转换为弧度(0° = 正北,顺时针)
	// 航空:0°=北、90°=东、180°=南、270°=西
	// 数学:0°=东、90°=北、180°=西、270°=南
	// 换算:math_angle = 90° - aviation_heading
	headingRad := (90 - aircraft.TargetHeading) * math.Pi / 180

	// 计算移动距离(速度单位:节,时间单位:秒)
	// 1 节 = 1 海里/小时 = 1/3600 海里/秒
	distanceNM := aircraft.TargetSpeed * deltaTime / 3600

	// 通过基本三角函数更新位置
	// 1 度纬度 ≈ 60 海里
	// 1 度经度 ≈ 60 * cos(纬度) 海里
	latChange := distanceNM * math.Sin(headingRad) / 60
	lonChange := distanceNM * math.Cos(headingRad) / (60 * math.Cos(aircraft.CurrentLat*math.Pi/180))

	aircraft.CurrentLat += latChange
	aircraft.CurrentLon += lonChange

	// 更新高度(垂直速率单位:英尺/分钟)
	aircraft.CurrentAltitude += aircraft.TargetVerticalRate * deltaTime / 60

	// 确保高度不会低于地面
	if aircraft.CurrentAltitude < 0 {
		aircraft.CurrentAltitude = 0
		aircraft.TargetVerticalRate = 0 // 触地后停止下降
	}
}

// generateUniqueHex 生成一个唯一的 6 位 hex 码
func (s *Service) generateUniqueHex() string {
	for {
		hex := fmt.Sprintf("%06X", rand.Intn(0xFFFFFF))
		// 确保不与已有飞行器冲突
		if _, exists := s.aircraft[hex]; !exists {
			return hex
		}
	}
}

// generateFlightNumber 生成形如 SIM001-SIM999 的航班号
func (s *Service) generateFlightNumber() string {
	return fmt.Sprintf("SIM%03d", rand.Intn(999)+1)
}

// IsSimulated 判断某 hex 码是否属于模拟飞行器
func (s *Service) IsSimulated(hex string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	_, exists := s.aircraft[hex]
	return exists
}
