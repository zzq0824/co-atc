package transcription

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yegors/co-atc/pkg/logger"
)

// FileLogger 处理仅追加方式将转写写入文件的日志
type FileLogger struct {
	baseDir string
	logger  *logger.Logger
	mu      sync.Mutex
	files   map[string]*os.File // 已打开文件句柄的缓存
}

// NewFileLogger 创建一个新的 FileLogger
// 如果 baseDir 为空,则禁用日志记录器,所有操作都是空操作
func NewFileLogger(baseDir string, logger *logger.Logger) (*FileLogger, error) {
	fl := &FileLogger{
		baseDir: baseDir,
		logger:  logger,
		files:   make(map[string]*os.File),
	}

	// 如果 baseDir 为空,则禁用日志记录
	if baseDir == "" {
		return fl, nil
	}

	// 创建基础目录及子目录
	rawDir := filepath.Join(baseDir, "raw")
	processedDir := filepath.Join(baseDir, "processed")

	if err := os.MkdirAll(rawDir, 0755); err != nil {
		return nil, fmt.Errorf("创建 raw 日志目录失败: %w", err)
	}
	if err := os.MkdirAll(processedDir, 0755); err != nil {
		return nil, fmt.Errorf("创建 processed 日志目录失败: %w", err)
	}

	logger.Info("已启用转写文件日志",
		String("base_dir", baseDir),
		String("raw_dir", rawDir),
		String("processed_dir", processedDir))

	return fl, nil
}

// IsEnabled 如果文件日志已启用,则返回 true
func (fl *FileLogger) IsEnabled() bool {
	return fl.baseDir != ""
}

// LogServiceStarted 将服务启动头写入 raw 和 processed 日志
func (fl *FileLogger) LogServiceStarted(frequencyID string) error {
	if !fl.IsEnabled() {
		return nil
	}

	now := time.Now().Local()
	timeStr := now.Format("15:04:05")
	header := fmt.Sprintf("[%s] TRANSCRIPTION SERVICE STARTED\n================================\n", timeStr)

	// 写入 raw 和 processed 日志
	if err := fl.writeHeader("raw", frequencyID, now, header); err != nil {
		return err
	}
	return fl.writeHeader("processed", frequencyID, now, header)
}

// LogRaw 将原始转写写入日志文件
func (fl *FileLogger) LogRaw(frequencyID string, timestamp time.Time, text string) error {
	if !fl.IsEnabled() {
		return nil
	}

	return fl.writeLog("raw", frequencyID, timestamp, text)
}

// LogProcessed 将已处理转写写入日志文件
func (fl *FileLogger) LogProcessed(frequencyID string, timestamp time.Time, speakerType, callsign, text string) error {
	if !fl.IsEnabled() {
		return nil
	}

	// 使用说话者信息格式化已处理条目
	var entry string
	if callsign != "" {
		entry = fmt.Sprintf("[%s] %s: %s", speakerType, callsign, text)
	} else {
		entry = fmt.Sprintf("[%s] %s", speakerType, text)
	}

	return fl.writeLog("processed", frequencyID, timestamp, entry)
}

// writeHeader 将头条目写入相应文件(无时间戳前缀)
func (fl *FileLogger) writeHeader(subDir, frequencyID string, timestamp time.Time, header string) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	// 根据日期和频率 ID 生成文件名
	localTimestamp := timestamp.Local()
	dateStr := localTimestamp.Format("2006-01-02")
	filename := fmt.Sprintf("%s_%s.log", dateStr, frequencyID)
	filePath := filepath.Join(fl.baseDir, subDir, filename)

	// 检查我们是否已缓存此文件
	cacheKey := filePath
	file, exists := fl.files[cacheKey]

	if !exists {
		// 以追加方式打开文件(若不存在则创建)
		var err error
		file, err = os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("打开日志文件 %s 失败: %w", filePath, err)
		}
		fl.files[cacheKey] = file
	}

	// 直接写入头部
	if _, err := file.WriteString(header); err != nil {
		return fmt.Errorf("写入日志文件 %s 失败: %w", filePath, err)
	}

	// 同步以确保数据已写入
	if err := file.Sync(); err != nil {
		fl.logger.Warn("同步日志文件失败",
			String("file", filePath),
			Error(err))
	}

	return nil
}

// writeLog 将日志条目写入相应文件
func (fl *FileLogger) writeLog(subDir, frequencyID string, timestamp time.Time, text string) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	// 根据日期和频率 ID 生成文件名
	localTimestamp := timestamp.Local()
	dateStr := localTimestamp.Format("2006-01-02")
	filename := fmt.Sprintf("%s_%s.log", dateStr, frequencyID)
	filePath := filepath.Join(fl.baseDir, subDir, filename)

	// 检查我们是否已缓存此文件
	cacheKey := filePath
	file, exists := fl.files[cacheKey]

	if !exists {
		// 以追加方式打开文件(若不存在则创建)
		var err error
		file, err = os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("打开日志文件 %s 失败: %w", filePath, err)
		}
		fl.files[cacheKey] = file
	}

	// 使用本机时间戳格式化日志条目
	timeStr := localTimestamp.Format("15:04:05")
	logEntry := fmt.Sprintf("[%s] %s\n", timeStr, text)

	// 写入文件
	if _, err := file.WriteString(logEntry); err != nil {
		return fmt.Errorf("写入日志文件 %s 失败: %w", filePath, err)
	}

	// 同步以确保数据已写入
	if err := file.Sync(); err != nil {
		fl.logger.Warn("同步日志文件失败",
			String("file", filePath),
			Error(err))
	}

	return nil
}

// Close 关闭所有打开的文件句柄
func (fl *FileLogger) Close() error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	var lastErr error
	for path, file := range fl.files {
		if err := file.Close(); err != nil {
			fl.logger.Error("关闭日志文件失败",
				String("file", path),
				Error(err))
			lastErr = err
		}
	}
	fl.files = make(map[string]*os.File)

	return lastErr
}

// CleanupOldFiles 清理不再当前的日期的文件句柄
// 应定期调用此函数以防止文件句柄累积
func (fl *FileLogger) CleanupOldFiles() {
	if !fl.IsEnabled() {
		return
	}

	fl.mu.Lock()
	defer fl.mu.Unlock()

	today := time.Now().Format("2006-01-02")

	for path, file := range fl.files {
		// 从文件名中提取日期
		filename := filepath.Base(path)
		if len(filename) >= 10 {
			fileDate := filename[:10]
			if fileDate != today {
				if err := file.Close(); err != nil {
					fl.logger.Warn("关闭旧日志文件失败",
						String("file", path),
						Error(err))
				}
				delete(fl.files, path)
				fl.logger.Debug("已关闭旧日志文件",
					String("file", path))
			}
		}
	}
}
