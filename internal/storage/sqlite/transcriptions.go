package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// 导入 logger 函数
var (
	String = logger.String
	Error  = logger.Error
)

// TranscriptionRecord 表示数据库中的转写记录
type TranscriptionRecord struct {
	ID               int64     `json:"id"`
	FrequencyID      string    `json:"frequency_id"`
	CreatedAt        time.Time `json:"created_at"`
	Content          string    `json:"content"`
	IsComplete       bool      `json:"is_complete"`
	IsProcessed      bool      `json:"is_processed"`
	ContentProcessed string    `json:"content_processed"`
	SpeakerType      string    `json:"speaker_type,omitempty"` // "ATC" 或 "PILOT"
	Callsign         string    `json:"callsign,omitempty"`     // 如果说话者是飞行员则为飞行器呼号
}

// TranscriptionStorage 处理转写记录的存储
type TranscriptionStorage struct {
	db     *sql.DB
	logger *logger.Logger
}

// NewTranscriptionStorage 创建一个新的 SQLite 转写存储
func NewTranscriptionStorage(db *sql.DB, logger *logger.Logger) *TranscriptionStorage {
	storage := &TranscriptionStorage{
		db:     db,
		logger: logger.Named("sqlite-tx"),
	}

	// 初始化数据库
	if err := storage.initDB(); err != nil {
		logger.Error("初始化转写存储失败", Error(err))
	}

	return storage
}

// initDB 初始化数据库表
func (s *TranscriptionStorage) initDB() error {
	// 创建 transcriptions 表
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS transcriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			frequency_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			content TEXT NOT NULL,
			is_complete BOOLEAN NOT NULL,
			is_processed BOOLEAN NOT NULL,
			content_processed TEXT,
			speaker_type TEXT,
			callsign TEXT
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 transcriptions 表失败: %w", err)
	}

	// 创建索引
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_frequency_id ON transcriptions(frequency_id)`)
	if err != nil {
		return fmt.Errorf("创建 frequency_id 索引失败: %w", err)
	}

	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_created_at ON transcriptions(created_at)`)
	if err != nil {
		return fmt.Errorf("创建 created_at 索引失败: %w", err)
	}

	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_speaker_type ON transcriptions(speaker_type)`)
	if err != nil {
		return fmt.Errorf("创建 speaker_type 索引失败: %w", err)
	}

	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_callsign ON transcriptions(callsign)`)
	if err != nil {
		return fmt.Errorf("创建 callsign 索引失败: %w", err)
	}

	return nil
}

// StoreTranscription 存储一条转写记录
func (s *TranscriptionStorage) StoreTranscription(record *TranscriptionRecord) (int64, error) {
	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	// 插入记录
	result, err := s.db.Exec(
		`INSERT INTO transcriptions
		(frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.FrequencyID,
		record.CreatedAt.Format(time.RFC3339),
		record.Content,
		record.IsComplete,
		record.IsProcessed,
		record.ContentProcessed,
		record.SpeakerType,
		record.Callsign,
	)
	if err != nil {
		return 0, fmt.Errorf("插入转写失败: %w", err)
	}

	// 获取 ID
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("获取最后插入 ID 失败: %w", err)
	}

	return id, nil
}

// GetTranscriptions 返回所有转写并支持分页
func (s *TranscriptionStorage) GetTranscriptions(limit, offset int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("查询转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerType, callsign sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerType,
			&callsign,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerType.Valid {
			record.SpeakerType = speakerType.String
		}
		if callsign.Valid {
			record.Callsign = callsign.String
		}

		records = append(records, &record)
	}

	return records, nil
}

// GetTranscriptionsByFrequency 返回特定频率的转写
func (s *TranscriptionStorage) GetTranscriptionsByFrequency(frequencyID string, limit, offset int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		WHERE frequency_id = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		frequencyID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("按频率查询转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerType, callsign sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerType,
			&callsign,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerType.Valid {
			record.SpeakerType = speakerType.String
		}
		if callsign.Valid {
			record.Callsign = callsign.String
		}

		records = append(records, &record)
	}

	return records, nil
}

// GetTranscriptionsByTimeRange 返回时间范围内的转写
func (s *TranscriptionStorage) GetTranscriptionsByTimeRange(startTime, endTime time.Time, limit, offset int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		WHERE created_at BETWEEN ? AND ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		startTime.Format(time.RFC3339), endTime.Format(time.RFC3339), limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("按时间范围查询转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerType, callsign sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerType,
			&callsign,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerType.Valid {
			record.SpeakerType = speakerType.String
		}
		if callsign.Valid {
			record.Callsign = callsign.String
		}

		records = append(records, &record)
	}

	return records, nil
}

// GetTranscriptionsBySpeaker 按说话者类型返回转写
func (s *TranscriptionStorage) GetTranscriptionsBySpeaker(speakerType string, limit, offset int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		WHERE speaker_type = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		speakerType, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("按说话者查询转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerTypeDB, callsign sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerTypeDB,
			&callsign,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerTypeDB.Valid {
			record.SpeakerType = speakerTypeDB.String
		}
		if callsign.Valid {
			record.Callsign = callsign.String
		}

		records = append(records, &record)
	}

	return records, nil
}

// GetTranscriptionsByCallsign 按飞行器呼号返回转写
func (s *TranscriptionStorage) GetTranscriptionsByCallsign(callsign string, limit, offset int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		WHERE callsign = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`,
		callsign, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("按呼号查询转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerType, callsignDB sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerType,
			&callsignDB,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerType.Valid {
			record.SpeakerType = speakerType.String
		}
		if callsignDB.Valid {
			record.Callsign = callsignDB.String
		}

		records = append(records, &record)
	}

	return records, nil
}

// GetUnprocessedTranscriptions 检索一批未处理的转写
func (s *TranscriptionStorage) GetUnprocessedTranscriptions(batchSize int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		WHERE is_complete = 1 AND is_processed = 0
		ORDER BY created_at ASC
		LIMIT ?`,
		batchSize,
	)
	if err != nil {
		return nil, fmt.Errorf("查询未处理的转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerType, callsign sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerType,
			&callsign,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerType.Valid {
			record.SpeakerType = speakerType.String
		}
		if callsign.Valid {
			record.Callsign = callsign.String
		}

		records = append(records, &record)
	}

	return records, nil
}

// UpdateProcessedTranscription 用已处理内容更新转写
func (s *TranscriptionStorage) UpdateProcessedTranscription(id int64, contentProcessed string, speakerType string, callsign string) error {
	lockSQLiteWrite()
	defer unlockSQLiteWrite()

	// 更新记录
	_, err := s.db.Exec(
		`UPDATE transcriptions
		SET content_processed = ?, is_processed = 1, speaker_type = ?, callsign = ?
		WHERE id = ?`,
		contentProcessed,
		speakerType,
		callsign,
		id,
	)
	if err != nil {
		return fmt.Errorf("更新已处理转写失败: %w", err)
	}

	return nil
}

// GetLastProcessedTranscriptions 检索给定频率的最后 N 条已处理转写
func (s *TranscriptionStorage) GetLastProcessedTranscriptions(frequencyID string, limit int) ([]*TranscriptionRecord, error) {
	// 查询记录
	rows, err := s.db.Query(
		`SELECT id, frequency_id, created_at, content, is_complete, is_processed, content_processed, speaker_type, callsign
		FROM transcriptions
		WHERE frequency_id = ? AND is_processed = 1
		ORDER BY created_at DESC
		LIMIT ?`,
		frequencyID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("查询最后已处理转写失败: %w", err)
	}
	defer rows.Close()

	// 解析记录
	var records []*TranscriptionRecord
	for rows.Next() {
		var record TranscriptionRecord
		var createdAt string
		var speakerType, callsign sql.NullString
		var contentProcessed sql.NullString

		if err := rows.Scan(
			&record.ID,
			&record.FrequencyID,
			&createdAt,
			&record.Content,
			&record.IsComplete,
			&record.IsProcessed,
			&contentProcessed,
			&speakerType,
			&callsign,
		); err != nil {
			return nil, fmt.Errorf("扫描转写失败: %w", err)
		}

		// 解析 created_at
		record.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("解析 created_at 失败: %w", err)
		}

		// 处理可空字段
		if contentProcessed.Valid {
			record.ContentProcessed = contentProcessed.String
		}
		if speakerType.Valid {
			record.SpeakerType = speakerType.String
		}
		if callsign.Valid {
			record.Callsign = callsign.String
		}

		records = append(records, &record)
	}

	return records, nil
}
