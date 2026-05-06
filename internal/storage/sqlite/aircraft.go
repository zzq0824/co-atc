package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/pkg/logger"
	_ "modernc.org/sqlite"
)

// AircraftRecord 表示用于上下文的飞行器记录
type AircraftRecord struct {
	Callsign     string
	Altitude     int
	TrueAirspeed int
}

// AircraftStorage 是基于 SQLite 的飞行器数据存储
type AircraftStorage struct {
	db     *sql.DB
	logger *logger.Logger
}

// NewAircraftStorage 创建一个新的基于 SQLite 的飞行器存储
func NewAircraftStorage(dbPath string, log *logger.Logger) (*AircraftStorage, error) {
	storageLogger := log.Named("sqlite")

	storageLogger.Info("正在初始化 SQLite 存储",
		logger.String("path", dbPath))

	// 在连接串中带上 pragma,以保证每个连接池中的连接都生效
	connStr := fmt.Sprintf("%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(10000)",
		dbPath,
	)
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 允许多个并发读取;写入通过包级 sqliteWriteMu 串行化
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	// 如不存在则创建表
	if err := initDatabase(db, storageLogger); err != nil {
		db.Close()
		return nil, err
	}

	storage := &AircraftStorage{
		db:     db,
		logger: storageLogger,
	}

	return storage, nil
}

// Close 关闭数据库连接
func (s *AircraftStorage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// GetDB 返回数据库连接
func (s *AircraftStorage) GetDB() *sql.DB {
	return s.db
}

// initDatabase 初始化数据库结构
func initDatabase(db *sql.DB, log *logger.Logger) error {
	log.Info("正在初始化数据库结构")

	// 创建包含核心字段的 aircraft 表
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS aircraft (
			hex TEXT PRIMARY KEY,
			flight TEXT,
			airline TEXT,
			status TEXT,
			last_seen TIMESTAMP,
			on_ground INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 aircraft 表失败: %w", err)
	}

	// 创建 adsb_targets 表,包含本地和外部 API 中可能出现的所有字段
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS adsb_targets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			aircraft_hex TEXT,
			hex TEXT,
			type TEXT,
			flight TEXT,
			registration TEXT,      -- 外部 API 专用字段 (r)
			aircraft_type TEXT,     -- 外部 API 专用字段 (t)
			alt_baro REAL,
			alt_geom REAL,
			gs REAL,
			ias REAL,
			tas REAL,
			mach REAL,
			wd REAL,
			ws REAL,
			oat REAL,
			tat REAL,
			track REAL,
			track_rate REAL,
			roll REAL,
			mag_heading REAL,
			true_heading REAL,
			baro_rate REAL,
			geom_rate REAL,
			squawk TEXT,
			emergency TEXT,
			category TEXT,
			nav_qnh REAL,
			nav_altitude_mcp REAL,
			nav_altitude_fms REAL,
			nav_heading REAL,
			nav_modes TEXT,
			lat REAL,
			lon REAL,
			nic INTEGER,
			rc INTEGER,
			seen_pos REAL,
			r_dst REAL,
			r_dir REAL,
			version INTEGER,
			nic_baro INTEGER,
			nac_p INTEGER,
			nac_v INTEGER,
			sil INTEGER,
			sil_type TEXT,
			gva INTEGER,
			sda INTEGER,
			alert INTEGER,
			spi INTEGER,
			mlat TEXT,
			tisb TEXT,
			messages INTEGER,
			seen REAL,
			rssi REAL,
			timestamp TIMESTAMP,
			raw_data TEXT,
			source_type TEXT,       -- 标识数据来源是 "local" 还是 "external"
			FOREIGN KEY (aircraft_hex) REFERENCES aircraft(hex) ON DELETE CASCADE,
			UNIQUE(aircraft_hex, lat, lon, alt_baro, gs, tas, track)
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 adsb_targets 表失败: %w", err)
	}

	// 创建 phase_changes 表,用于跟踪飞行阶段切换
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS phase_changes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			hex TEXT NOT NULL,
			flight TEXT,
			phase TEXT NOT NULL,
			timestamp TIMESTAMP NOT NULL,
			adsb_id INTEGER,
			FOREIGN KEY (adsb_id) REFERENCES adsb_targets(id),
			FOREIGN KEY (hex) REFERENCES aircraft(hex) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 phase_changes 表失败: %w", err)
	}

	// 创建索引以提升查询效率
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_adsb_targets_aircraft_hex ON adsb_targets(aircraft_hex)`)
	if err != nil {
		return fmt.Errorf("创建 adsb_targets.aircraft_hex 索引失败: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_adsb_targets_timestamp ON adsb_targets(timestamp)`)
	if err != nil {
		return fmt.Errorf("创建 adsb_targets.timestamp 索引失败: %w", err)
	}

	// 用于高效获取最新记录的关键复合索引
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_adsb_targets_hex_timestamp ON adsb_targets(aircraft_hex, timestamp DESC)`)
	if err != nil {
		return fmt.Errorf("创建 adsb_targets.aircraft_hex_timestamp 索引失败: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_aircraft_status ON aircraft(status)`)
	if err != nil {
		return fmt.Errorf("创建 aircraft.status 索引失败: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_aircraft_last_seen ON aircraft(last_seen)`)
	if err != nil {
		return fmt.Errorf("创建 aircraft.last_seen 索引失败: %w", err)
	}

	// 创建 phase_changes 表的索引
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_phase_changes_hex_timestamp ON phase_changes(hex, timestamp)`)
	if err != nil {
		return fmt.Errorf("创建 phase_changes.hex_timestamp 索引失败: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_phase_changes_phase_timestamp ON phase_changes(phase, timestamp)`)
	if err != nil {
		return fmt.Errorf("创建 phase_changes.phase_timestamp 索引失败: %w", err)
	}

	// phase + hex 查询的索引(用于起飞/着陆时间查找)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_phase_changes_hex_phase_timestamp ON phase_changes(hex, phase, timestamp DESC)`)
	if err != nil {
		return fmt.Errorf("创建 phase_changes.hex_phase_timestamp 索引失败: %w", err)
	}

	// isUniqueADSBTarget 中唯一性检查所用的覆盖索引
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_adsb_targets_unique_check ON adsb_targets(aircraft_hex, lat, lon, alt_baro, gs, tas, track)`)
	if err != nil {
		return fmt.Errorf("创建 adsb_targets 唯一性检查索引失败: %w", err)
	}

	log.Info("数据库结构初始化成功")
	return nil
}

// GetAll 返回所有飞行器
func (s *AircraftStorage) GetAll() []*adsb.Aircraft {
	aircraft, err := s.getAllAircraftInternal(0, false)
	if err != nil {
		s.logger.Error("获取所有飞行器失败", logger.Error(err))
		return []*adsb.Aircraft{}
	}

	return aircraft
}

// GetAllWithLastSeenFilter 返回最近 N 分钟内出现过的飞行器
// 该过滤在数据库层进行,在大型数据库中性能显著优于内存过滤
func (s *AircraftStorage) GetAllWithLastSeenFilter(lastSeenMinutes int) []*adsb.Aircraft {
	aircraft, err := s.getAllAircraftInternal(lastSeenMinutes, false)
	if err != nil {
		s.logger.Error("按最近出现时间过滤获取飞行器失败", logger.Error(err))
		return []*adsb.Aircraft{}
	}

	return aircraft
}

// GetAllMinimal 以最简数据返回飞行器(跳过阶段历史与日期查询)
// 该方法针对 simple=1 的 API 端点做了优化
func (s *AircraftStorage) GetAllMinimal(lastSeenMinutes int) []*adsb.Aircraft {
	aircraft, err := s.getAllAircraftInternal(lastSeenMinutes, true)
	if err != nil {
		s.logger.Error("获取最简飞行器数据失败", logger.Error(err))
		return []*adsb.Aircraft{}
	}

	return aircraft
}

// getAllAircraftInternal 从数据库读取飞行器,可选地进行过滤
// 当 lastSeenMinutes > 0 时,仅返回该时间窗口内出现过的飞行器
// 当 minimal 为 true 时,跳过阶段历史与起降时间查询(simple API 更快)
func (s *AircraftStorage) getAllAircraftInternal(lastSeenMinutes int, minimal bool) ([]*adsb.Aircraft, error) {
	start := time.Now()
	s.logger.Debug("开始执行 getAllAircraftInternal 查询", logger.Int("lastSeenMinutes", lastSeenMinutes))

	// 根据是否带 last_seen 过滤构建查询
	var rows *sql.Rows
	var err error

	if lastSeenMinutes > 0 {
		// 使用参数化的截止时间,可命中 idx_aircraft_last_seen 索引
		cutoffTime := time.Now().UTC().Add(-time.Duration(lastSeenMinutes) * time.Minute)
		rows, err = s.db.Query(`
			SELECT hex, flight, airline, status, last_seen,
			on_ground, created_at
			FROM aircraft
			WHERE last_seen >= ?
		`, cutoffTime.Format(time.RFC3339))
	} else {
		// 查询所有飞行器(原有行为)
		rows, err = s.db.Query(`
			SELECT hex, flight, airline, status, last_seen,
			on_ground, created_at
			FROM aircraft
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("查询飞行器失败: %w", err)
	}
	defer rows.Close()

	// 用于按 hex 存放飞行器的 map
	aircraftMap := make(map[string]*adsb.Aircraft)

	// 处理飞行器行
	for rows.Next() {
		var a adsb.Aircraft
		var lastSeen, createdAt string
		var onGround int

		if err := rows.Scan(
			&a.Hex, &a.Flight, &a.Airline, &a.Status, &lastSeen,
			&onGround, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("扫描飞行器行失败: %w", err)
		}

		// 整型转为布尔型
		a.OnGround = onGround != 0

		// 解析 last_seen 时间戳
		t, err := time.Parse(time.RFC3339, lastSeen)
		if err != nil {
			return nil, fmt.Errorf("解析 last_seen 时间戳失败: %w", err)
		}
		a.LastSeen = t

		// 解析 created_at 时间戳
		createdTime, err := time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 时间戳失败: %w", err)
		}
		a.CreatedAt = createdTime

		// 初始化空的历史与预测切片(主飞行器接口不填充)
		a.History = []adsb.PositionMinimal{}
		a.Future = []adsb.Position{}
		a.Hindcast = []adsb.Position{}

		// 加入 map
		aircraftMap[a.Hex] = &a
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代飞行器行时出错: %w", err)
	}

	// 未查到任何飞行器时返回空切片
	if len(aircraftMap) == 0 {
		return []*adsb.Aircraft{}, nil
	}

	// 为后续批处理统一构建 hex 列表
	hexCodes := make([]string, 0, len(aircraftMap))
	for hex := range aircraftMap {
		hexCodes = append(hexCodes, hex)
	}

	// 性能:对 ADSB 数据使用批量查询,而非 N 次单独查询
	adsbStart := time.Now()
	s.logger.Debug("开始填充 ADSB 数据(批量)", logger.Int("aircraft_count", len(aircraftMap)))

	adsbDataMap, err := s.GetLatestADSBDataBatch(hexCodes)
	if err != nil {
		s.logger.Error("批量获取 ADSB 数据失败", logger.Error(err))
	} else {
		for hex, aircraft := range aircraftMap {
			if adsbData, exists := adsbDataMap[hex]; exists {
				aircraft.ADSB = adsbData
			}
		}
	}

	adsbDuration := time.Since(adsbStart)
	s.logger.Debug("ADSB 数据填充完成", logger.Duration("duration", adsbDuration))

	// 通过单次批量查询获取所有飞行器的当前阶段
	phaseStart := time.Now()
	s.logger.Debug("开始填充阶段数据", logger.Int("aircraft_count", len(aircraftMap)), logger.Bool("minimal", minimal))

	currentPhases, err := s.GetCurrentPhasesBatch(hexCodes)
	if err != nil {
		s.logger.Error("批量获取当前阶段失败", logger.Error(err))
	} else {
		// 最简模式下跳过阶段历史查询(仅需当前阶段)
		var recentHistory map[string][]adsb.PhaseChange
		if !minimal {
			// 批量获取所有飞行器的近期阶段历史(每架最近 5 条)
			recentHistory, err = s.getRecentPhaseHistoryBatch(hexCodes, 5)
			if err != nil {
				s.logger.Error("批量获取近期阶段历史失败", logger.Error(err))
				recentHistory = make(map[string][]adsb.PhaseChange) // 空回退
			}
		} else {
			recentHistory = make(map[string][]adsb.PhaseChange)
		}

		// 将当前阶段与近期历史挂载到飞行器
		for hex, aircraft := range aircraftMap {
			if phase, exists := currentPhases[hex]; exists {
				history := recentHistory[hex] // 未命中时为空切片
				aircraft.Phase = &adsb.PhaseData{
					Current: []adsb.PhaseChange{*phase},
					History: history,
				}
			}
		}
	}

	phaseDuration := time.Since(phaseStart)
	s.logger.Debug("阶段数据填充完成", logger.Duration("duration", phaseDuration))

	// 性能:批量查询起飞和着陆时间(最简模式下跳过)
	if !minimal {
		dateStart := time.Now()
		s.logger.Debug("开始填充 date_landed/date_tookoff(批量)", logger.Int("aircraft_count", len(aircraftMap)))

		takeoffTimes, err := s.GetLatestTakeoffTimesBatch(hexCodes)
		if err != nil {
			s.logger.Error("批量获取起飞时间失败", logger.Error(err))
		} else {
			for hex, aircraft := range aircraftMap {
				if takeoffTime, exists := takeoffTimes[hex]; exists {
					aircraft.DateTookoff = takeoffTime
				}
			}
		}

		landingTimes, err := s.GetLatestLandingTimesBatch(hexCodes)
		if err != nil {
			s.logger.Error("批量获取着陆时间失败", logger.Error(err))
		} else {
			for hex, aircraft := range aircraftMap {
				if landingTime, exists := landingTimes[hex]; exists {
					aircraft.DateLanded = landingTime
				}
			}
		}

		dateDuration := time.Since(dateStart)
		s.logger.Debug("日期数据填充完成(批量)", logger.Duration("duration", dateDuration))
	}

	// 将 map 转换为切片
	aircraft := make([]*adsb.Aircraft, 0, len(aircraftMap))
	for _, a := range aircraftMap {
		aircraft = append(aircraft, a)
	}

	totalDuration := time.Since(start)
	s.logger.Debug("getAllAircraft 完成",
		logger.Duration("total_duration", totalDuration),
		logger.Int("aircraft_count", len(aircraft)))

	return aircraft, nil
}

// getLatestADSBData 返回某架飞行器最新的 ADSB 数据
func (s *AircraftStorage) getLatestADSBData(hex string) (*adsb.ADSBTarget, error) {
	row := s.db.QueryRow(`
		SELECT raw_data, source_type, registration, aircraft_type FROM adsb_targets
		WHERE aircraft_hex = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`, hex)

	var rawDataJSON, sourceType, registration, aircraftType string
	if err := row.Scan(&rawDataJSON, &sourceType, &registration, &aircraftType); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	var rawData adsb.ADSBTarget
	if err := json.Unmarshal([]byte(rawDataJSON), &rawData); err != nil {
		return nil, err
	}

	// 设置 source type、注册号、机型字段
	rawData.SourceType = sourceType
	rawData.Registration = registration
	rawData.AircraftType = aircraftType

	return &rawData, nil
}

// GetLatestADSBDataBatch 通过单次查询返回多架飞行器的最新 ADSB 数据
func (s *AircraftStorage) GetLatestADSBDataBatch(hexCodes []string) (map[string]*adsb.ADSBTarget, error) {
	start := time.Now()
	s.logger.Debug("开始批量 ADSB 查询", logger.Int("hex_count", len(hexCodes)))

	if len(hexCodes) == 0 {
		return make(map[string]*adsb.ADSBTarget), nil
	}

	// 为 IN 子句构建占位符(查询需要两份副本)
	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes)*2) // 双份参数:子查询用一次,外层查询用一次
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex               // 子查询所用的第一组
		args[i+len(hexCodes)] = hex // 外层 WHERE 所用的第二组
	}

	// 采用 GROUP BY + JOIN 模式,在大表上比相关子查询效率更高
	// 1. 子查询按飞行器分组找到 MAX(timestamp)(结果集很小)
	// 2. JOIN 借助索引取出真正的数据行
	query := fmt.Sprintf(`
		SELECT
			a.aircraft_hex,
			a.raw_data,
			a.source_type,
			a.registration,
			a.aircraft_type
		FROM adsb_targets a
		INNER JOIN (
			SELECT aircraft_hex, MAX(timestamp) as max_ts
			FROM adsb_targets
			WHERE aircraft_hex IN (%s)
			GROUP BY aircraft_hex
		) latest ON a.aircraft_hex = latest.aircraft_hex AND a.timestamp = latest.max_ts
		WHERE a.aircraft_hex IN (%s)
	`, strings.Join(placeholders, ","), strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询最新 ADSB 数据失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*adsb.ADSBTarget)

	for rows.Next() {
		var hex, rawDataJSON, sourceType, registration, aircraftType string
		if err := rows.Scan(&hex, &rawDataJSON, &sourceType, &registration, &aircraftType); err != nil {
			return nil, fmt.Errorf("扫描 ADSB 数据行失败: %w", err)
		}

		var rawData adsb.ADSBTarget
		if err := json.Unmarshal([]byte(rawDataJSON), &rawData); err != nil {
			s.logger.Error("反序列化 ADSB 数据失败", logger.Error(err), logger.String("hex", hex))
			continue
		}

		// 设置 source type、注册号、机型字段
		rawData.SourceType = sourceType
		rawData.Registration = registration
		rawData.AircraftType = aircraftType

		result[hex] = &rawData
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代 ADSB 数据行时出错: %w", err)
	}

	duration := time.Since(start)
	s.logger.Debug("批量 ADSB 查询完成",
		logger.Duration("duration", duration),
		logger.Int("requested_count", len(hexCodes)),
		logger.Int("returned_count", len(result)))

	return result, nil
}

// getRecentPhaseHistoryBatch 通过单次查询返回多架飞行器的近期阶段历史
func (s *AircraftStorage) getRecentPhaseHistoryBatch(hexCodes []string, limit int) (map[string][]adsb.PhaseChange, error) {
	if len(hexCodes) == 0 {
		return make(map[string][]adsb.PhaseChange), nil
	}

	// 为 IN 子句构建占位符
	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes)+1)
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex
	}
	args[len(hexCodes)] = limit // 将 limit 作为最后一个参数加入

	// 获取每架飞行器近期阶段历史的查询
	query := fmt.Sprintf(`
		SELECT hex, id, phase, timestamp, adsb_id
		FROM (
			SELECT
				hex, id, phase, timestamp, adsb_id,
				ROW_NUMBER() OVER (PARTITION BY hex ORDER BY timestamp DESC) as rn
			FROM phase_changes
			WHERE hex IN (%s)
		) ranked
		WHERE rn <= ?
		ORDER BY hex, timestamp DESC
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询近期阶段历史失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]adsb.PhaseChange)

	for rows.Next() {
		var hex, phase, timestampStr string
		var id int
		var adsbId sql.NullInt64

		if err := rows.Scan(&hex, &id, &phase, &timestampStr, &adsbId); err != nil {
			return nil, fmt.Errorf("扫描阶段历史行失败: %w", err)
		}

		timestamp, err := time.Parse(time.RFC3339, timestampStr)
		if err != nil {
			s.logger.Error("解析阶段时间戳失败", logger.Error(err), logger.String("hex", hex))
			continue
		}

		phaseChange := adsb.PhaseChange{
			ID:        id,
			Phase:     phase,
			Timestamp: timestamp,
		}

		if adsbId.Valid {
			adsbIdInt := int(adsbId.Int64)
			phaseChange.ADSBId = &adsbIdInt
		}

		result[hex] = append(result[hex], phaseChange)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代阶段历史行时出错: %w", err)
	}

	return result, nil
}

// getPositionHistoryMinimal 返回用于地图轨迹的最简位置历史(lat、lon、alt_baro、timestamp)
func (s *AircraftStorage) getPositionHistoryMinimal(hex string) ([]adsb.PositionMinimal, error) {
	//s.logger.Debug("正在获取最简位置历史",
	//	logger.String("hex", hex),
	//	logger.Int("maxPositions", maxPositions))

	rows, err := s.db.Query(`
		SELECT lat, lon, alt_baro, timestamp
		FROM adsb_targets
		WHERE aircraft_hex = ?
		ORDER BY timestamp DESC
	`, hex)

	if err != nil {
		s.logger.Error("查询位置历史出错", logger.Error(err), logger.String("hex", hex))
		return nil, err
	}
	defer rows.Close()

	positions := []adsb.PositionMinimal{}
	for rows.Next() {
		var pos adsb.PositionMinimal
		var timestamp string
		var lat, lon, alt sql.NullFloat64

		if err := rows.Scan(&lat, &lon, &alt, &timestamp); err != nil {
			s.logger.Error("扫描位置行出错", logger.Error(err), logger.String("hex", hex))
			return nil, err
		}

		// 没有真实位置的行直接跳过,避免伪造零坐标。
		if !lat.Valid || !lon.Valid {
			continue
		}
		pos.Lat = lat.Float64
		pos.Lon = lon.Float64
		if alt.Valid {
			pos.AltBaro = alt.Float64
		}

		t, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			s.logger.Error("解析时间戳出错", logger.Error(err), logger.String("hex", hex))
			return nil, err
		}
		pos.Timestamp = t

		positions = append(positions, pos)
	}

	// 反向排序使其按时间正序排列
	for i, j := 0, len(positions)-1; i < j; i, j = i+1, j-1 {
		positions[i], positions[j] = positions[j], positions[i]
	}

	return positions, nil
}

// getPositionHistory 返回某架飞行器的完整位置历史
func (s *AircraftStorage) getPositionHistory(hex string, maxPositions int) ([]adsb.Position, error) {
	// 使用配置的 maxPositions 参数
	rows, err := s.db.Query(`
		SELECT id, lat, lon, alt_baro, gs, tas, track, true_heading, mag_heading, timestamp, registration, aircraft_type, source_type
		FROM adsb_targets
		WHERE aircraft_hex = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, hex, maxPositions)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	positions := []adsb.Position{}
	for rows.Next() {
		var pos adsb.Position
		var id int
		var timestamp, registration, aircraftType, sourceType string
		var lat, lon, altitude sql.NullFloat64
		var speedGS, speedTrue sql.NullFloat64
		var track, trueHeading, magHeading sql.NullFloat64

		if err := rows.Scan(&id, &lat, &lon, &altitude, &speedGS, &speedTrue, &track, &trueHeading, &magHeading, &timestamp,
			&registration, &aircraftType, &sourceType); err != nil {
			return nil, err
		}

		if !lat.Valid || !lon.Valid {
			continue
		}
		pos.Lat = nullFloatPtr(lat)
		pos.Lon = nullFloatPtr(lon)
		if altitude.Valid {
			pos.Altitude = nullFloatPtr(altitude)
		}

		if speedGS.Valid {
			pos.SpeedGS = nullFloatPtr(speedGS)
		}
		if speedTrue.Valid {
			pos.SpeedTrue = nullFloatPtr(speedTrue)
		}

		if track.Valid {
			v := track.Float64
			pos.Track = &v
		}
		if trueHeading.Valid {
			v := trueHeading.Float64
			pos.TrueHeading = &v
		}
		if magHeading.Valid {
			v := magHeading.Float64
			pos.MagHeading = &v
		}

		// 设置 ID 字段
		pos.ID = &id

		t, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return nil, err
		}
		pos.Timestamp = t

		// 向位置附加元数据
		metadata := make(map[string]string)
		if registration != "" {
			metadata["registration"] = registration
		}
		if aircraftType != "" {
			metadata["aircraft_type"] = aircraftType
		}
		if sourceType != "" {
			metadata["source_type"] = sourceType
		}

		// 调试用:记录外部数据
		if sourceType == "external-rapidapi" && (registration != "" || aircraftType != "") {
			s.logger.Debug("含外部数据的位置",
				logger.String("hex", hex),
				logger.String("registration", registration),
				logger.String("aircraft_type", aircraftType),
				logger.Time("timestamp", t))
		}

		positions = append(positions, pos)
	}

	// 反向排序使其按时间正序排列
	for i, j := 0, len(positions)-1; i < j; i, j = i+1, j-1 {
		positions[i], positions[j] = positions[j], positions[i]
	}

	return positions, nil
}

// GetAllPositionHistory 返回某架飞行器最近 1 小时的位置历史,按时间戳降序排列
func (s *AircraftStorage) GetAllPositionHistory(hex string) ([]adsb.Position, error) {
	// 计算 1 小时前的 RFC3339 格式时间戳(与存储时相同的格式)
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)

	// 查询该飞行器最近 1 小时的位置,按时间戳降序(最新优先)排列
	rows, err := s.db.Query(`
		SELECT id, lat, lon, alt_baro, gs, tas, true_heading, mag_heading, baro_rate, timestamp, registration, aircraft_type, source_type
		FROM adsb_targets
		WHERE aircraft_hex = ? AND timestamp >= ?
		ORDER BY timestamp DESC
	`, hex, oneHourAgo)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	positions := []adsb.Position{}
	for rows.Next() {
		var pos adsb.Position
		var id int
		var timestamp, registration, aircraftType, sourceType string
		var lat, lon, altitude sql.NullFloat64
		var speedGS, speedTrue, trueHeading, magHeading, verticalSpeed sql.NullFloat64

		if err := rows.Scan(&id, &lat, &lon, &altitude, &speedGS, &speedTrue, &trueHeading, &magHeading, &verticalSpeed, &timestamp,
			&registration, &aircraftType, &sourceType); err != nil {
			return nil, err
		}

		if !lat.Valid || !lon.Valid {
			continue
		}
		pos.Lat = nullFloatPtr(lat)
		pos.Lon = nullFloatPtr(lon)
		if altitude.Valid {
			pos.Altitude = nullFloatPtr(altitude)
		}

		if speedGS.Valid {
			pos.SpeedGS = nullFloatPtr(speedGS)
		}
		if speedTrue.Valid {
			pos.SpeedTrue = nullFloatPtr(speedTrue)
		}
		if trueHeading.Valid {
			v := trueHeading.Float64
			pos.TrueHeading = &v
		}
		if magHeading.Valid {
			v := magHeading.Float64
			pos.MagHeading = &v
		}
		if verticalSpeed.Valid {
			pos.VerticalSpeed = nullFloatPtr(verticalSpeed)
		}

		// 设置 ID 字段
		pos.ID = &id

		t, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return nil, err
		}
		pos.Timestamp = t

		// 向位置附加元数据
		metadata := make(map[string]string)
		if registration != "" {
			metadata["registration"] = registration
		}
		if aircraftType != "" {
			metadata["aircraft_type"] = aircraftType
		}
		if sourceType != "" {
			metadata["source_type"] = sourceType
		}

		// 调试用:记录外部数据
		if sourceType == "external-rapidapi" && (registration != "" || aircraftType != "") {
			s.logger.Debug("含外部数据的位置",
				logger.String("hex", hex),
				logger.String("registration", registration),
				logger.String("aircraft_type", aircraftType),
				logger.Time("timestamp", t))
		}

		positions = append(positions, pos)
	}

	return positions, nil
}

// GetPositionHistoryWithLimit 返回某架飞行器的位置历史,按时间戳降序排列,附带条数上限
func (s *AircraftStorage) GetPositionHistoryWithLimit(hex string, limit int) ([]adsb.Position, error) {
	// 计算 1 小时前的 RFC3339 格式时间戳(与存储时相同的格式)
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)

	// 查询该飞行器最近 1 小时的位置,按时间戳降序(最新优先)排列,带 limit
	rows, err := s.db.Query(`
		SELECT id, lat, lon, alt_baro, gs, tas, track, true_heading, mag_heading, baro_rate, timestamp, registration, aircraft_type, source_type
		FROM adsb_targets
		WHERE aircraft_hex = ? AND timestamp >= ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, hex, oneHourAgo, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	positions := []adsb.Position{}
	for rows.Next() {
		var pos adsb.Position
		var id int
		var timestamp, registration, aircraftType, sourceType string
		var lat, lon, altitude sql.NullFloat64
		var speedGS, speedTrue, verticalSpeed sql.NullFloat64
		var track, trueHeading, magHeading sql.NullFloat64

		if err := rows.Scan(&id, &lat, &lon, &altitude, &speedGS, &speedTrue, &track, &trueHeading, &magHeading, &verticalSpeed, &timestamp,
			&registration, &aircraftType, &sourceType); err != nil {
			return nil, err
		}

		if !lat.Valid || !lon.Valid {
			continue
		}
		pos.Lat = nullFloatPtr(lat)
		pos.Lon = nullFloatPtr(lon)
		if altitude.Valid {
			pos.Altitude = nullFloatPtr(altitude)
		}

		if speedGS.Valid {
			pos.SpeedGS = nullFloatPtr(speedGS)
		}
		if speedTrue.Valid {
			pos.SpeedTrue = nullFloatPtr(speedTrue)
		}

		if track.Valid {
			v := track.Float64
			pos.Track = &v
		}
		if trueHeading.Valid {
			v := trueHeading.Float64
			pos.TrueHeading = &v
		}
		if magHeading.Valid {
			v := magHeading.Float64
			pos.MagHeading = &v
		}
		if verticalSpeed.Valid {
			pos.VerticalSpeed = nullFloatPtr(verticalSpeed)
		}

		// 设置 ID 字段
		pos.ID = &id

		t, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return nil, err
		}
		pos.Timestamp = t

		// 注意:Position 结构体没有 metadata 字段,这里暂时跳过
		_ = registration // 避免“变量未使用”警告
		_ = aircraftType
		_ = sourceType

		positions = append(positions, pos)
	}

	return positions, nil
}

// GetByHex 通过 hex ID 返回一架飞行器
func (s *AircraftStorage) GetByHex(hex string) (*adsb.Aircraft, bool) {

	// 查询飞行器
	row := s.db.QueryRow(`
		SELECT hex, flight, airline, status, last_seen, on_ground
		FROM aircraft
		WHERE hex = ?
	`, hex)

	var a adsb.Aircraft
	var lastSeen string
	var onGround int

	if err := row.Scan(
		&a.Hex, &a.Flight, &a.Airline, &a.Status, &lastSeen, &onGround,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, false
		}
		s.logger.Error("扫描飞行器行失败", logger.Error(err), logger.String("hex", hex))
		return nil, false
	}

	// 解析 last_seen 时间戳
	t, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		s.logger.Error("解析 last_seen 时间戳失败", logger.Error(err), logger.String("hex", hex))
		return nil, false
	}
	a.LastSeen = t

	// 整型转为布尔型
	a.OnGround = onGround != 0

	// 获取最新的 ADSB 数据
	adsbData, err := s.getLatestADSBData(hex)
	if err == nil && adsbData != nil {
		a.ADSB = adsbData
	}

	// 获取用于地图轨迹的最简位置历史
	minimalPositions, err := s.getPositionHistoryMinimal(hex)
	if err == nil {
		a.History = minimalPositions
	} else {
		a.History = []adsb.PositionMinimal{}
		s.logger.Error("获取位置历史失败", logger.Error(err), logger.String("hex", hex))
	}

	// 当具备所需数据时计算预测位置
	if a.ADSB != nil && a.ADSB.HasPosition() && a.ADSB.AltBaro.Float64() != 0 {
		lat, lon, _ := a.ADSB.Position()
		// 取航向(优先 true_heading,其次 track,再次 mag_heading)
		heading := adsb.NumberOrZero(a.ADSB.TrueHeading)
		if heading == 0 {
			heading = adsb.NumberOrZero(a.ADSB.Track)
		}
		if heading == 0 {
			heading = adsb.NumberOrZero(a.ADSB.MagHeading)
		}

		// 取速度(TAS 或 GS,任一可用)
		speed := adsb.NumberOrZero(a.ADSB.TAS)
		if speed == 0 {
			speed = adsb.NumberOrZero(a.ADSB.GS)
		}

		// 取垂直速率(baro_rate 或 geom_rate,任一可用)
		verticalRate := adsb.NumberOrZero(a.ADSB.BaroRate)
		if verticalRate == 0 {
			verticalRate = adsb.NumberOrZero(a.ADSB.GeomRate)
		}

		// 仅在航向与速度都有效时进行预测
		if heading != 0 && speed != 0 {
			// 计算预测位置
			// 获取用于预测的磁航向
			magHeading := adsb.NumberOrZero(a.ADSB.MagHeading)
			if magHeading == 0 {
				magHeading = heading // 回退为已取到的任意航向
			}

			a.Future = adsb.PredictFuturePositions(
				lat,
				lon,
				a.ADSB.AltBaro.Float64(),
				heading,    // 真航向
				magHeading, // 磁航向
				speed,
				verticalRate,
			)
		} else {
			// 初始化空的预测切片
			a.Future = []adsb.Position{}
		}
	} else {
		// 初始化空的预测切片
		a.Future = []adsb.Position{}
	}
	a.Hindcast = []adsb.Position{}

	// 填充该飞行器的阶段数据
	if err := s.populatePhaseData(&a); err != nil {
		s.logger.Error("填充阶段数据失败", logger.Error(err), logger.String("hex", hex))
		// 即便阶段数据失败,仍然返回飞行器
	}

	// 从 phase_changes 表填充 DateLanded 和 DateTookoff 字段
	takeoffTime, err := s.GetLatestTakeoffTime(hex)
	if err != nil {
		s.logger.Error("获取最近一次起飞时间失败", logger.Error(err), logger.String("hex", hex))
	} else {
		a.DateTookoff = takeoffTime
	}

	landingTime, err := s.GetLatestLandingTime(hex)
	if err != nil {
		s.logger.Error("获取最近一次着陆时间失败", logger.Error(err), logger.String("hex", hex))
	} else {
		a.DateLanded = landingTime
	}

	return &a, true
}

// Upsert 更新或插入一架飞行器
func (s *AircraftStorage) Upsert(aircraft *adsb.Aircraft) {
	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	const maxBusyRetries = 3
	for attempt := 0; attempt <= maxBusyRetries; attempt++ {
		err := s.upsertOnce(aircraft)
		if err == nil {
			return
		}

		if !isSQLiteBusyError(err) || attempt == maxBusyRetries {
			s.logger.Error("飞行器 upsert 失败",
				logger.Error(err),
				logger.String("hex", aircraft.Hex),
				logger.Int("attempt", attempt+1))
			return
		}

		backoff := time.Duration((attempt+1)*100) * time.Millisecond
		s.logger.Warn("飞行器 upsert 期间 SQLite 繁忙,正在重试",
			logger.String("hex", aircraft.Hex),
			logger.Int("attempt", attempt+1),
			logger.Int("backoff_ms", int(backoff/time.Millisecond)))
		time.Sleep(backoff)
	}
}

func (s *AircraftStorage) upsertOnce(aircraft *adsb.Aircraft) (err error) {
	// 确保所有时间戳都使用 UTC
	aircraft.LastSeen = aircraft.LastSeen.UTC()

	// 开启事务
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			s.logger.Error("回滚事务失败", logger.Error(rollbackErr), logger.String("hex", aircraft.Hex))
		}
	}()

	// 判断飞行器是否已存在
	var exists bool
	err = tx.QueryRow("SELECT 1 FROM aircraft WHERE hex = ?", aircraft.Hex).Scan(&exists)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("判断飞行器是否存在: %w", err)
	}

	// 新数据默认状态置为 active
	if aircraft.Status == "" {
		aircraft.Status = "active"
	}

	if err == sql.ErrNoRows {
		// 以 UTC 时间戳插入新飞行器
		now := time.Now().UTC().Format(time.RFC3339)
		_, err = tx.Exec(`
			INSERT INTO aircraft (
				hex, flight, airline, status, last_seen,
				on_ground, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
			aircraft.Hex, aircraft.Flight, aircraft.Airline, aircraft.Status,
			aircraft.LastSeen.Format(time.RFC3339),
			boolToInt(aircraft.OnGround),
			now, now,
		)
		if err != nil {
			return fmt.Errorf("插入飞行器: %w", err)
		}
	} else {
		// 以 UTC 时间戳更新已有飞行器的 updated_at
		now := time.Now().UTC().Format(time.RFC3339)
		_, err = tx.Exec(`
			UPDATE aircraft SET
				flight = ?, airline = ?, status = ?, last_seen = ?, on_ground = ?, updated_at = ?
			WHERE hex = ?
		`,
			aircraft.Flight, aircraft.Airline, aircraft.Status, aircraft.LastSeen.Format(time.RFC3339),
			boolToInt(aircraft.OnGround), now, aircraft.Hex,
		)
		if err != nil {
			return fmt.Errorf("更新飞行器: %w", err)
		}
	}

	if aircraft.ADSB != nil {
		// 将 ADSB 数据序列化为 JSON
		rawData, err := json.Marshal(aircraft.ADSB)
		if err != nil {
			return fmt.Errorf("序列化 ADSB 数据: %w", err)
		}

		// 直接从 ADSB 数据获取数据来源、注册号和机型
		sourceType := "local"
		registration := ""
		aircraftType := ""

		if aircraft.ADSB.SourceType != "" {
			sourceType = aircraft.ADSB.SourceType
		}

		if aircraft.ADSB.Registration != "" {
			registration = aircraft.ADSB.Registration
		}

		if aircraft.ADSB.AircraftType != "" {
			aircraftType = aircraft.ADSB.AircraftType
		}

		// 插入 ADSB 目标(通过 UNIQUE 约束 + OR IGNORE 实现去重)
		_, err = tx.Exec(`
			INSERT OR IGNORE INTO adsb_targets (
				aircraft_hex, hex, type, flight, registration, aircraft_type, alt_baro, alt_geom, gs, ias, tas, mach, wd, ws, oat, tat,
				track, track_rate, roll, mag_heading, true_heading, baro_rate, geom_rate, squawk, emergency,
				category, nav_qnh, nav_altitude_mcp, nav_altitude_fms, nav_heading, nav_modes, lat, lon,
				nic, rc, seen_pos, r_dst, r_dir, version, nic_baro, nac_p, nac_v, sil, sil_type, gva, sda,
				alert, spi, mlat, tisb, messages, seen, rssi, timestamp, raw_data, source_type
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)
		`,
			aircraft.Hex, aircraft.ADSB.Hex, aircraft.ADSB.Type, aircraft.ADSB.Flight,
			registration, aircraftType,
			nullableFlexibleFloatValue(aircraft.ADSB.AltBaro), nullableFlexibleFloatValue(aircraft.ADSB.AltGeom), nullableFloatValue(aircraft.ADSB.GS), nullableFloatValue(aircraft.ADSB.IAS),
			nullableFloatValue(aircraft.ADSB.TAS), nullableFloatValue(aircraft.ADSB.Mach), nullableFloatValue(aircraft.ADSB.WD), nullableFloatValue(aircraft.ADSB.WS),
			nullableFloatValue(aircraft.ADSB.OAT), nullableFloatValue(aircraft.ADSB.TAT), aircraft.ADSB.Track, nullableFloatValue(aircraft.ADSB.TrackRate),
			nullableFloatValue(aircraft.ADSB.Roll), aircraft.ADSB.MagHeading, aircraft.ADSB.TrueHeading,
			nullableFloatValue(aircraft.ADSB.BaroRate), nullableFloatValue(aircraft.ADSB.GeomRate), aircraft.ADSB.Squawk,
			"", aircraft.ADSB.Category, nullableFloatValue(aircraft.ADSB.NavQNH),
			nullableFloatValue(aircraft.ADSB.NavAltitudeMCP), nullableFloatValue(aircraft.ADSB.NavAltitudeFMS), nullableFloatValue(aircraft.ADSB.NavHeading),
			"", nullableFloatValue(aircraft.ADSB.Lat), nullableFloatValue(aircraft.ADSB.Lon),
			nullableIntValue(aircraft.ADSB.NIC), nullableIntValue(aircraft.ADSB.RC), nullableFloatValue(aircraft.ADSB.SeenPos), nullableFloatValue(aircraft.ADSB.RDst),
			nullableFloatValue(aircraft.ADSB.RDir), nullableIntValue(aircraft.ADSB.Version), nullableIntValue(aircraft.ADSB.NICBaro), nullableIntValue(aircraft.ADSB.NACP),
			nullableIntValue(aircraft.ADSB.NACV), nullableIntValue(aircraft.ADSB.SIL), aircraft.ADSB.SILType, nullableIntValue(aircraft.ADSB.GVA),
			nullableIntValue(aircraft.ADSB.SDA), nullableIntValue(aircraft.ADSB.Alert), nullableIntValue(aircraft.ADSB.SPI),
			"", "",
			nullableIntValue(aircraft.ADSB.Messages), nullableFloatValue(aircraft.ADSB.Seen), nullableFloatValue(aircraft.ADSB.RSSI),
			aircraft.LastSeen.Format(time.RFC3339), string(rawData), sourceType,
		)
		if err != nil {
			return fmt.Errorf("插入 ADSB 目标: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("提交事务: %w", err)
	}

	return nil
}

// Count 返回数据库中的飞行器数量
func (s *AircraftStorage) Count() int {

	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM aircraft").Scan(&count)
	if err != nil {
		s.logger.Error("飞行器计数失败", logger.Error(err))
		return 0
	}

	return count
}

// GetFiltered 按高度、状态和日期范围过滤后返回飞行器
func (s *AircraftStorage) GetFiltered(
	minAltitude, maxAltitude float64,
	status []string,
	tookOffAfter, tookOffBefore, landedAfter, landedBefore *time.Time,
) []*adsb.Aircraft {

	// 使用占位符构建查询
	query := `
		SELECT hex, flight, airline, status, last_seen, on_ground
		FROM aircraft
		WHERE 1=1`

	// 创建用于存放查询参数的切片
	args := []interface{}{}

	// 如提供了 status 过滤条件则加入
	if len(status) > 0 {
		query += " AND status IN (" + strings.Repeat("?,", len(status)-1) + "?)"
		for _, s := range status {
			args = append(args, s)
		}
	}

	// TODO: 通过对 phase_changes 表进行 JOIN(T/O 与 T/D 阶段)来补充日期过滤
	// 目前先忽略日期过滤,后续需基于新的 phase_changes 表实现
	_ = tookOffAfter
	_ = tookOffBefore
	_ = landedAfter
	_ = landedBefore

	// 执行查询
	rows, err := s.db.Query(query, args...)
	if err != nil {
		s.logger.Error("查询过滤后的飞行器失败", logger.Error(err))
		return []*adsb.Aircraft{}
	}
	defer rows.Close()

	// 用于按 hex 存放飞行器的 map
	aircraftMap := make(map[string]*adsb.Aircraft)

	// 处理飞行器行
	for rows.Next() {
		var a adsb.Aircraft
		var lastSeen string
		var onGround int

		if err := rows.Scan(
			&a.Hex, &a.Flight, &a.Airline, &a.Status, &lastSeen, &onGround,
		); err != nil {
			s.logger.Error("扫描飞行器行失败", logger.Error(err))
			continue
		}

		// 解析 last_seen 时间戳
		t, err := time.Parse(time.RFC3339, lastSeen)
		if err != nil {
			s.logger.Error("解析 last_seen 时间戳失败", logger.Error(err))
			continue
		}
		a.LastSeen = t

		// 整型转为布尔型
		a.OnGround = onGround != 0

		// 初始化空的历史切片
		a.History = []adsb.PositionMinimal{}

		// 加入 map
		aircraftMap[a.Hex] = &a
	}

	if err := rows.Err(); err != nil {
		s.logger.Error("迭代飞行器行时出错", logger.Error(err))
		return []*adsb.Aircraft{}
	}

	// 未查到任何飞行器时返回空切片
	if len(aircraftMap) == 0 {
		return []*adsb.Aircraft{}
	}

	// 为每架飞行器获取最新的 ADSB 数据与位置历史
	for hex, aircraft := range aircraftMap {
		// 获取最新的 ADSB 数据
		adsbData, err := s.getLatestADSBData(hex)
		if err == nil && adsbData != nil {
			aircraft.ADSB = adsbData
		}

		// 过滤接口不填充历史数据
		// 请改用合并的 /aircraft/{hex}/tracks 端点

		// 填充该飞行器的阶段数据
		if err := s.populatePhaseData(aircraft); err != nil {
			s.logger.Error("填充阶段数据失败", logger.Error(err), logger.String("hex", hex))
			// 即便阶段数据失败也继续处理其他飞行器
		}
	}

	// 将 map 转换为切片
	aircraft := make([]*adsb.Aircraft, 0, len(aircraftMap))
	for _, a := range aircraftMap {
		aircraft = append(aircraft, a)
	}

	return aircraft
}

// boolToInt 将布尔值转换为整型(true 转 1,false 转 0)
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableFloatValue(v *float64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func nullableIntValue(v *int) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func nullableFlexibleFloatValue(v adsb.FlexibleFloat64) interface{} {
	return v.NullableValue()
}

func nullFloatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

// formatNullableTime 将可空的 time.Time 格式化为 SQL 用值
func formatNullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

// marshalStringArray 将字符串数组转换为 JSON 字符串以便存储
func marshalStringArray(arr []string) string {
	if arr == nil || len(arr) == 0 {
		return ""
	}

	data, err := json.Marshal(arr)
	if err != nil {
		return ""
	}

	return string(data)
}

// GetActiveAircraft 获取活跃飞行器的数据
func (s *AircraftStorage) GetActiveAircraft() ([]*AircraftRecord, error) {

	// 查询活跃飞行器及其最新位置数据
	rows, err := s.db.Query(`
		SELECT a.flight, t.alt_baro, t.tas
		FROM aircraft a
		LEFT JOIN (
			SELECT aircraft_hex, alt_baro, tas,
				ROW_NUMBER() OVER (PARTITION BY aircraft_hex ORDER BY timestamp DESC) as rn
			FROM adsb_targets
		) t ON t.aircraft_hex = a.hex AND t.rn = 1
		WHERE a.status = 'active'
		ORDER BY a.flight ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询活跃飞行器失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var aircraft []*AircraftRecord
	for rows.Next() {
		var record AircraftRecord
		var callsign sql.NullString
		var altitude, trueAirspeed sql.NullFloat64

		if err := rows.Scan(&callsign, &altitude, &trueAirspeed); err != nil {
			return nil, fmt.Errorf("扫描飞行器失败: %w", err)
		}

		// 处理可空字段
		if callsign.Valid {
			record.Callsign = callsign.String
		}
		if altitude.Valid {
			record.Altitude = int(altitude.Float64)
		}
		if trueAirspeed.Valid {
			record.TrueAirspeed = int(trueAirspeed.Float64)
		}

		aircraft = append(aircraft, &record)
	}

	return aircraft, nil
}

// InsertPhaseChange 插入一条新的阶段变更记录
func (s *AircraftStorage) InsertPhaseChange(hex, flight, phase string, timestamp time.Time, adsbId *int) error {
	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	_, err := s.db.Exec(`
		INSERT INTO phase_changes (hex, flight, phase, timestamp, adsb_id)
		VALUES (?, ?, ?, ?, ?)
	`, hex, flight, phase, timestamp.Format(time.RFC3339), adsbId)

	if err != nil {
		s.logger.Error("插入阶段变更失败", logger.Error(err),
			logger.String("hex", hex), logger.String("phase", phase))
		return fmt.Errorf("插入阶段变更失败: %w", err)
	}

	return nil
}

// GetPhaseHistory 返回某架飞行器的所有阶段变更,按时间戳降序排列
func (s *AircraftStorage) GetPhaseHistory(hex string) ([]adsb.PhaseChange, error) {

	rows, err := s.db.Query(`
		SELECT id, phase, timestamp, adsb_id
		FROM phase_changes
		WHERE hex = ?
		ORDER BY timestamp DESC
	`, hex)
	if err != nil {
		return nil, fmt.Errorf("查询阶段历史失败: %w", err)
	}
	defer rows.Close()

	var phases []adsb.PhaseChange
	for rows.Next() {
		var phase adsb.PhaseChange
		var timestampStr string
		var adsbId sql.NullInt64

		if err := rows.Scan(&phase.ID, &phase.Phase, &timestampStr, &adsbId); err != nil {
			return nil, fmt.Errorf("扫描阶段变更行失败: %w", err)
		}

		// 调试日志,用于查看从数据库读取的数据
		//s.logger.Debug("从数据库扫描到的阶段变更",
		//	logger.String("hex", hex),
		//	logger.Int("id", phase.ID),
		//	logger.String("phase", phase.Phase),
		//	logger.String("timestamp", timestampStr))

		// 解析时间戳
		timestamp, err := time.Parse(time.RFC3339, timestampStr)
		if err != nil {
			return nil, fmt.Errorf("解析时间戳失败: %w", err)
		}
		phase.Timestamp = timestamp

		// 处理可空的 adsb_id
		if adsbId.Valid {
			id := int(adsbId.Int64)
			phase.ADSBId = &id
		}

		phases = append(phases, phase)
	}

	return phases, nil
}

// GetCurrentPhase 返回某架飞行器最新的阶段
func (s *AircraftStorage) GetCurrentPhase(hex string) (*adsb.PhaseChange, error) {

	row := s.db.QueryRow(`
		SELECT id, phase, timestamp, adsb_id
		FROM phase_changes
		WHERE hex = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`, hex)

	var phase adsb.PhaseChange
	var timestampStr string
	var adsbId sql.NullInt64

	if err := row.Scan(&phase.ID, &phase.Phase, &timestampStr, &adsbId); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // 未找到阶段变更记录
		}
		return nil, fmt.Errorf("扫描当前阶段失败: %w", err)
	}

	// 解析时间戳
	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		return nil, fmt.Errorf("解析时间戳失败: %w", err)
	}
	phase.Timestamp = timestamp

	// 处理可空的 adsb_id
	if adsbId.Valid {
		id := int(adsbId.Int64)
		phase.ADSBId = &id
	}

	return &phase, nil
}

// GetLatestTakeoffTime 从 phase_changes 中返回某架飞行器最近的起飞时间
func (s *AircraftStorage) GetLatestTakeoffTime(hex string) (*time.Time, error) {

	row := s.db.QueryRow(`
		SELECT timestamp
		FROM phase_changes
		WHERE hex = ? AND phase = 'T/O'
		ORDER BY timestamp DESC
		LIMIT 1
	`, hex)

	var timestampStr string
	if err := row.Scan(&timestampStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // 未找到起飞记录
		}
		return nil, fmt.Errorf("扫描起飞时间失败: %w", err)
	}

	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		return nil, fmt.Errorf("解析起飞时间戳失败: %w", err)
	}

	return &timestamp, nil
}

// GetLatestLandingTime 从 phase_changes 中返回某架飞行器最近的着陆时间
func (s *AircraftStorage) GetLatestLandingTime(hex string) (*time.Time, error) {

	row := s.db.QueryRow(`
		SELECT timestamp
		FROM phase_changes
		WHERE hex = ? AND phase = 'T/D'
		ORDER BY timestamp DESC
		LIMIT 1
	`, hex)

	var timestampStr string
	if err := row.Scan(&timestampStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // 未找到着陆记录
		}
		return nil, fmt.Errorf("扫描着陆时间失败: %w", err)
	}

	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		return nil, fmt.Errorf("解析着陆时间戳失败: %w", err)
	}

	return &timestamp, nil
}

// populatePhaseData 从 phase_changes 表为飞行器填充阶段数据
func (s *AircraftStorage) populatePhaseData(aircraft *adsb.Aircraft) error {
	start := time.Now()

	// 获取该飞行器的阶段历史
	phaseHistory, err := s.GetPhaseHistory(aircraft.Hex)
	if err != nil {
		return fmt.Errorf("获取阶段历史失败: %w", err)
	}

	// 调试日志,查看 GetPhaseHistory 的返回情况
	//s.logger.Debug("已获取阶段历史",
	//	logger.String("hex", aircraft.Hex),
	//	logger.Int("count", len(phaseHistory)))

	// 构建阶段数据结构
	phaseData := &adsb.PhaseData{
		Current: []adsb.PhaseChange{},
		History: phaseHistory,
	}

	// 设置当前阶段(取历史第一项;若无历史则为空)
	if len(phaseHistory) > 0 {
		phaseData.Current = []adsb.PhaseChange{phaseHistory[0]}
		s.logger.Debug("已设置当前阶段",
			logger.String("hex", aircraft.Hex),
			logger.Int("current_id", phaseHistory[0].ID),
			logger.String("current_phase", phaseHistory[0].Phase))
	}

	aircraft.Phase = phaseData

	// 从 phase_changes 表获取起飞与着陆时间
	takeoffTime, err := s.GetLatestTakeoffTime(aircraft.Hex)
	if err != nil {
		s.logger.Error("获取起飞时间失败", logger.Error(err), logger.String("hex", aircraft.Hex))
	} else {
		aircraft.DateTookoff = takeoffTime
	}

	landingTime, err := s.GetLatestLandingTime(aircraft.Hex)
	if err != nil {
		s.logger.Error("获取着陆时间失败", logger.Error(err), logger.String("hex", aircraft.Hex))
	} else {
		aircraft.DateLanded = landingTime
	}

	duration := time.Since(start)
	if duration > 10*time.Millisecond {
		s.logger.Debug("阶段数据填充较慢",
			logger.String("hex", aircraft.Hex),
			logger.Duration("duration", duration))
	}

	return nil
}

// GetLatestADSBTargetID 返回某架飞行器最新的 ADSB 目标记录 ID
func (s *AircraftStorage) GetLatestADSBTargetID(hex string) (*int, error) {

	row := s.db.QueryRow(`
		SELECT id
		FROM adsb_targets
		WHERE aircraft_hex = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`, hex)

	var id int
	if err := row.Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // 未找到 ADSB 目标
		}
		return nil, fmt.Errorf("扫描 ADSB 目标 ID 失败: %w", err)
	}

	return &id, nil
}

// GetCurrentPhasesBatch 通过单次查询返回多架飞行器的当前阶段
func (s *AircraftStorage) GetCurrentPhasesBatch(hexCodes []string) (map[string]*adsb.PhaseChange, error) {
	if len(hexCodes) == 0 {
		return make(map[string]*adsb.PhaseChange), nil
	}

	// 为 IN 子句构建占位符(查询需要两份副本)
	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes)*2)
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex
		args[i+len(hexCodes)] = hex
	}

	// 采用 GROUP BY + JOIN 模式,性能优于 ROW_NUMBER()
	query := fmt.Sprintf(`
		SELECT p.hex, p.id, p.phase, p.timestamp, p.adsb_id
		FROM phase_changes p
		INNER JOIN (
			SELECT hex, MAX(timestamp) as max_ts
			FROM phase_changes
			WHERE hex IN (%s)
			GROUP BY hex
		) latest ON p.hex = latest.hex AND p.timestamp = latest.max_ts
		WHERE p.hex IN (%s)
	`, strings.Join(placeholders, ","), strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询当前阶段失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*adsb.PhaseChange)
	for rows.Next() {
		var hex, phase, timestampStr string
		var id int
		var adsbId *int

		if err := rows.Scan(&hex, &id, &phase, &timestampStr, &adsbId); err != nil {
			return nil, fmt.Errorf("扫描阶段行失败: %w", err)
		}

		timestamp, err := time.Parse(time.RFC3339, timestampStr)
		if err != nil {
			return nil, fmt.Errorf("解析时间戳失败: %w", err)
		}

		result[hex] = &adsb.PhaseChange{
			ID:        id,
			Phase:     phase,
			Timestamp: timestamp,
			ADSBId:    adsbId,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代阶段行时出错: %w", err)
	}

	return result, nil
}

// GetLatestADSBTargetIDsBatch 通过单次查询返回多架飞行器最新的 ADSB 目标 ID
func (s *AircraftStorage) GetLatestADSBTargetIDsBatch(hexCodes []string) (map[string]*int, error) {
	if len(hexCodes) == 0 {
		return make(map[string]*int), nil
	}

	// 为 IN 子句构建占位符(查询需要两份副本)
	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes)*2)
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex
		args[i+len(hexCodes)] = hex
	}

	// 采用 GROUP BY + JOIN 模式,性能优于 ROW_NUMBER()
	query := fmt.Sprintf(`
		SELECT a.aircraft_hex, a.id
		FROM adsb_targets a
		INNER JOIN (
			SELECT aircraft_hex, MAX(timestamp) as max_ts
			FROM adsb_targets
			WHERE aircraft_hex IN (%s)
			GROUP BY aircraft_hex
		) latest ON a.aircraft_hex = latest.aircraft_hex AND a.timestamp = latest.max_ts
		WHERE a.aircraft_hex IN (%s)
	`, strings.Join(placeholders, ","), strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询最新 ADSB 目标 ID 失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*int)
	for rows.Next() {
		var hex string
		var id int

		if err := rows.Scan(&hex, &id); err != nil {
			return nil, fmt.Errorf("扫描 ADSB 目标 ID 行失败: %w", err)
		}

		result[hex] = &id
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代 ADSB 目标 ID 行时出错: %w", err)
	}

	return result, nil
}

// InsertPhaseChangesBatch 在单个事务内插入多条阶段变更
func (s *AircraftStorage) InsertPhaseChangesBatch(changes []adsb.PhaseChangeInsert) error {
	if len(changes) == 0 {
		return nil
	}

	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	// 开启事务
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()

	// 准备插入语句
	stmt, err := tx.Prepare(`
		INSERT INTO phase_changes (hex, flight, phase, timestamp, adsb_id)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("准备阶段变更插入语句失败: %w", err)
	}
	defer stmt.Close()

	// 插入所有阶段变更
	for _, change := range changes {
		_, err := stmt.Exec(
			change.Hex,
			change.Flight,
			change.Phase,
			change.Timestamp.Format(time.RFC3339),
			change.ADSBId,
		)
		if err != nil {
			return fmt.Errorf("插入 %s 的阶段变更失败: %w", change.Hex, err)
		}
	}

	// 提交事务
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交阶段变更批量插入失败: %w", err)
	}

	s.logger.Debug("批量插入阶段变更完成",
		logger.Int("count", len(changes)))

	return nil
}

// GetLatestTakeoffTimesBatch 通过单次查询返回多架飞行器最近的起飞时间
func (s *AircraftStorage) GetLatestTakeoffTimesBatch(hexCodes []string) (map[string]*time.Time, error) {
	if len(hexCodes) == 0 {
		return make(map[string]*time.Time), nil
	}

	// 为 IN 子句构建占位符
	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes))
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex
	}

	// 使用 GROUP BY 配合 MAX——按 hex 获取最新时间戳的最简高效写法
	// 命中 idx_phase_changes_hex_phase_timestamp 索引
	query := fmt.Sprintf(`
		SELECT hex, MAX(timestamp) as timestamp
		FROM phase_changes
		WHERE hex IN (%s) AND phase = 'T/O'
		GROUP BY hex
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询最近起飞时间失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*time.Time)
	for rows.Next() {
		var hex, timestampStr string

		if err := rows.Scan(&hex, &timestampStr); err != nil {
			return nil, fmt.Errorf("扫描起飞时间行失败: %w", err)
		}

		timestamp, err := time.Parse(time.RFC3339, timestampStr)
		if err != nil {
			s.logger.Error("解析起飞时间戳失败", logger.Error(err), logger.String("hex", hex))
			continue
		}

		result[hex] = &timestamp
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代起飞时间行时出错: %w", err)
	}

	return result, nil
}

// GetLatestLandingTimesBatch 通过单次查询返回多架飞行器最近的着陆时间
func (s *AircraftStorage) GetLatestLandingTimesBatch(hexCodes []string) (map[string]*time.Time, error) {
	if len(hexCodes) == 0 {
		return make(map[string]*time.Time), nil
	}

	// 为 IN 子句构建占位符
	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes))
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex
	}

	// 使用 GROUP BY 配合 MAX——按 hex 获取最新时间戳的最简高效写法
	// 命中 idx_phase_changes_hex_phase_timestamp 索引
	query := fmt.Sprintf(`
		SELECT hex, MAX(timestamp) as timestamp
		FROM phase_changes
		WHERE hex IN (%s) AND phase = 'T/D'
		GROUP BY hex
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询最近着陆时间失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*time.Time)
	for rows.Next() {
		var hex, timestampStr string

		if err := rows.Scan(&hex, &timestampStr); err != nil {
			return nil, fmt.Errorf("扫描着陆时间行失败: %w", err)
		}

		timestamp, err := time.Parse(time.RFC3339, timestampStr)
		if err != nil {
			s.logger.Error("解析着陆时间戳失败", logger.Error(err), logger.String("hex", hex))
			continue
		}

		result[hex] = &timestamp
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代着陆时间行时出错: %w", err)
	}

	return result, nil
}

// GetStaleActiveAircraft 返回不再广播、需要更新状态的飞行器。
// 过滤条件:status='active',hex NOT IN activeHexCodes,last_seen < cutoff。
// 返回的飞行器已附带最新的 ADSB 数据。
func (s *AircraftStorage) GetStaleActiveAircraft(activeHexCodes []string, cutoff time.Time) ([]*adsb.Aircraft, error) {
	// 构建 NOT IN 子句
	args := make([]interface{}, 0, len(activeHexCodes)+1)
	notInClause := ""
	if len(activeHexCodes) > 0 {
		placeholders := make([]string, len(activeHexCodes))
		for i, hex := range activeHexCodes {
			placeholders[i] = "?"
			args = append(args, hex)
		}
		notInClause = fmt.Sprintf("AND hex NOT IN (%s)", strings.Join(placeholders, ","))
	}
	args = append(args, cutoff.Format(time.RFC3339))

	query := fmt.Sprintf(`
		SELECT hex, flight, airline, status, last_seen, on_ground
		FROM aircraft
		WHERE status = 'active' %s AND last_seen < ?
	`, notInClause)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询陈旧的活跃飞行器失败: %w", err)
	}
	defer rows.Close()

	var aircraft []*adsb.Aircraft
	var hexCodes []string
	for rows.Next() {
		var a adsb.Aircraft
		var lastSeen string
		var onGround int
		if err := rows.Scan(&a.Hex, &a.Flight, &a.Airline, &a.Status, &lastSeen, &onGround); err != nil {
			return nil, fmt.Errorf("扫描陈旧飞行器行失败: %w", err)
		}
		a.OnGround = onGround != 0
		t, err := time.Parse(time.RFC3339, lastSeen)
		if err != nil {
			return nil, fmt.Errorf("解析 last_seen 失败: %w", err)
		}
		a.LastSeen = t
		aircraft = append(aircraft, &a)
		hexCodes = append(hexCodes, a.Hex)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代陈旧飞行器行时出错: %w", err)
	}
	rows.Close()

	// 关闭 rows 游标后再批量获取 ADSB 数据(避免 SQLite 单连接死锁)
	if len(hexCodes) > 0 {
		adsbDataMap, err := s.GetLatestADSBDataBatch(hexCodes)
		if err == nil {
			for _, a := range aircraft {
				if adsbData, exists := adsbDataMap[a.Hex]; exists {
					a.ADSB = adsbData
				}
			}
		}
	}

	return aircraft, nil
}

// GetAircraftOnGroundBatch 通过单次查询返回多架飞行器的 on_ground 状态。
// 返回 map 中 hex -> on_ground 仅包含数据库中存在的飞行器。
// map 中缺失的键表示该飞行器尚未存在。
func (s *AircraftStorage) GetAircraftOnGroundBatch(hexCodes []string) (map[string]bool, error) {
	if len(hexCodes) == 0 {
		return make(map[string]bool), nil
	}

	placeholders := make([]string, len(hexCodes))
	args := make([]interface{}, len(hexCodes))
	for i, hex := range hexCodes {
		placeholders[i] = "?"
		args[i] = hex
	}

	query := fmt.Sprintf(`
		SELECT hex, on_ground FROM aircraft WHERE hex IN (%s)
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("批量查询飞行器 on_ground 状态失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var hex string
		var onGround int
		if err := rows.Scan(&hex, &onGround); err != nil {
			return nil, fmt.Errorf("扫描飞行器 on_ground 行失败: %w", err)
		}
		result[hex] = onGround != 0
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代飞行器 on_ground 行时出错: %w", err)
	}

	return result, nil
}
