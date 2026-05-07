package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// ClearanceStorage 处理许可记录的存储
type ClearanceStorage struct {
	db     *sql.DB
	logger *logger.Logger
}

// NewClearanceStorage 创建一个新的 SQLite 许可存储
func NewClearanceStorage(db *sql.DB, logger *logger.Logger) *ClearanceStorage {
	storage := &ClearanceStorage{
		db:     db,
		logger: logger.Named("sqlite-clearances"),
	}

	// 初始化数据库
	if err := storage.initDB(); err != nil {
		logger.Error("初始化许可存储失败", Error(err))
	}

	return storage
}

// initDB 初始化数据库表
func (s *ClearanceStorage) initDB() error {
	// 创建 clearances 表
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS clearances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transcription_id INTEGER NOT NULL,
			callsign TEXT NOT NULL,
			clearance_type TEXT NOT NULL,
			clearance_text TEXT NOT NULL,
			runway TEXT,
			timestamp TIMESTAMP NOT NULL,
			status TEXT NOT NULL DEFAULT 'issued',
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY (transcription_id) REFERENCES transcriptions(id)
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 clearances 表失败: %w", err)
	}

	// 创建索引以提升性能
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_clearances_callsign ON clearances(callsign)`,
		`CREATE INDEX IF NOT EXISTS idx_clearances_timestamp ON clearances(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_clearances_type ON clearances(clearance_type)`,
		`CREATE INDEX IF NOT EXISTS idx_clearances_status ON clearances(status)`,
		`CREATE INDEX IF NOT EXISTS idx_clearances_transcription_id ON clearances(transcription_id)`,
	}

	for _, indexSQL := range indexes {
		_, err = s.db.Exec(indexSQL)
		if err != nil {
			return fmt.Errorf("创建许可索引失败: %w", err)
		}
	}

	return nil
}

// StoreClearance 存储一条许可记录
func (s *ClearanceStorage) StoreClearance(record *ClearanceRecord) (int64, error) {
	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	// 插入记录
	result, err := s.db.Exec(
		`INSERT INTO clearances
		(transcription_id, callsign, clearance_type, clearance_text, runway, timestamp, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.TranscriptionID,
		record.Callsign,
		record.ClearanceType,
		record.ClearanceText,
		record.Runway,
		record.Timestamp.Format(time.RFC3339),
		record.Status,
		record.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("插入许可失败: %w", err)
	}

	// 获取 ID
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("获取最后插入 ID 失败: %w", err)
	}

	return id, nil
}

// GetClearancesByCallsign 返回特定飞行器呼号的许可
func (s *ClearanceStorage) GetClearancesByCallsign(callsign string, limit int) ([]*ClearanceRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, transcription_id, callsign, clearance_type, clearance_text, runway, timestamp, status, created_at
		FROM clearances
		WHERE callsign = ?
		ORDER BY timestamp DESC
		LIMIT ?`,
		callsign, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("按呼号查询许可失败: %w", err)
	}
	defer rows.Close()

	return s.scanClearanceRows(rows)
}

// GetClearancesByTimeRange 返回时间范围内的许可
func (s *ClearanceStorage) GetClearancesByTimeRange(startTime, endTime time.Time) ([]*ClearanceRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, transcription_id, callsign, clearance_type, clearance_text, runway, timestamp, status, created_at
		FROM clearances
		WHERE timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC`,
		startTime.Format(time.RFC3339), endTime.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("按时间范围查询许可失败: %w", err)
	}
	defer rows.Close()

	return s.scanClearanceRows(rows)
}

// GetClearancesByType 返回特定类型的许可
func (s *ClearanceStorage) GetClearancesByType(clearanceType string, limit int) ([]*ClearanceRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, transcription_id, callsign, clearance_type, clearance_text, runway, timestamp, status, created_at
		FROM clearances
		WHERE clearance_type = ?
		ORDER BY timestamp DESC
		LIMIT ?`,
		clearanceType, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("按类型查询许可失败: %w", err)
	}
	defer rows.Close()

	return s.scanClearanceRows(rows)
}

// UpdateClearanceStatus 更新许可的状态(用于第二阶段合规监控)
func (s *ClearanceStorage) UpdateClearanceStatus(id int64, status string) error {
	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	// 更新记录
	_, err := s.db.Exec(
		`UPDATE clearances
		SET status = ?
		WHERE id = ?`,
		status,
		id,
	)
	if err != nil {
		return fmt.Errorf("更新许可状态失败: %w", err)
	}

	return nil
}

// GetRecentClearances 返回所有飞行器最近的许可
func (s *ClearanceStorage) GetRecentClearances(limit int) ([]*ClearanceRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, transcription_id, callsign, clearance_type, clearance_text, runway, timestamp, status, created_at
		FROM clearances
		ORDER BY timestamp DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("查询最近的许可失败: %w", err)
	}
	defer rows.Close()

	return s.scanClearanceRows(rows)
}

// scanClearanceRows 将数据库行扫描到 ClearanceRecord 结构体中
func (s *ClearanceStorage) scanClearanceRows(rows *sql.Rows) ([]*ClearanceRecord, error) {
	var records []*ClearanceRecord
	for rows.Next() {
		var record ClearanceRecord
		var timestamp, createdAt string
		var runway sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.TranscriptionID,
			&record.Callsign,
			&record.ClearanceType,
			&record.ClearanceText,
			&runway,
			&timestamp,
			&record.Status,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("扫描许可失败: %w", err)
		}

		// 解析时间戳
		var err error
		record.Timestamp, err = time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return nil, fmt.Errorf("解析 timestamp 失败: %w", err)
		}

		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空的 runway 字段
		if runway.Valid {
			record.Runway = runway.String
		}

		records = append(records, &record)
	}

	return records, nil
}
