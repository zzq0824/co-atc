package logger

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// zap 字段的字段别名
type Field = zapcore.Field

// 用于创建字段的辅助函数
var (
	// String 创建一个字符串值字段
	String = zap.String
	// Int 创建一个 int 值字段
	Int = zap.Int
	// Int64 创建一个 int64 值字段
	Int64 = zap.Int64
	// Float64 创建一个 float64 值字段
	Float64 = zap.Float64
	// Bool 创建一个 bool 值字段
	Bool = zap.Bool
	// Time 创建一个 time.Time 值字段
	Time = zap.Time
	// Duration 创建一个 time.Duration 值字段
	Duration = zap.Duration
	// Error 创建一个错误值字段
	Error = zap.Error
	// Any 创建一个任意值字段
	Any = zap.Any
)

// Logger 是 zap.Logger 的封装
type Logger struct {
	*zap.Logger
}

// Config 表示 logger 配置
type Config struct {
	Level  string // debug、info、warn、error
	Format string // json、console
}

// 自定义级别编码器,为控制台输出添加颜色
func coloredLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch level {
	case zapcore.ErrorLevel:
		enc.AppendString("\033[1;31m" + level.String() + "\033[0m") // 加粗红色
	case zapcore.WarnLevel:
		enc.AppendString("\033[1;33m" + level.String() + "\033[0m") // 加粗黄色
	case zapcore.InfoLevel:
		enc.AppendString("\033[1;36m" + level.String() + "\033[0m") // 加粗青色
	case zapcore.DebugLevel:
		enc.AppendString("\033[1;37m" + level.String() + "\033[0m") // 加粗白色
	default:
		enc.AppendString(level.String())
	}
}

// 自定义名称编码器,将 logger 名称截断或填充至固定宽度
func fixedWidthNameEncoder(loggerName string, enc zapcore.PrimitiveArrayEncoder) {
	// 提取 logger 名称的最后一段以便更短的显示
	parts := strings.Split(loggerName, ".")
	displayName := parts[len(parts)-1]

	// 限制为 15 字符并以空格填充以对齐列
	if len(displayName) > 15 {
		displayName = displayName[:15]
	} else if len(displayName) < 15 {
		displayName = displayName + strings.Repeat(" ", 15-len(displayName))
	}

	enc.AppendString(displayName)
}

// New 使用给定的配置创建一个新的 logger
func New(config Config) (*Logger, error) {
	// 解析日志级别
	level, err := parseLogLevel(config.Level)
	if err != nil {
		return nil, err
	}

	// 创建编码器配置
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	}

	// 根据格式设置编码选项
	if config.Format == "console" {
		encoderConfig.EncodeLevel = coloredLevelEncoder
		encoderConfig.EncodeName = fixedWidthNameEncoder
	} else {
		encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		encoderConfig.EncodeName = zapcore.FullNameEncoder
	}

	// 仅在 debug 级别包含调用者信息
	// 其他级别则忽略调用者信息
	if level != zapcore.DebugLevel {
		encoderConfig.CallerKey = zapcore.OmitKey
	} else {
		encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	}

	// 根据格式创建编码器
	var encoder zapcore.Encoder
	switch config.Format {
	case "json":
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	case "console":
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	default:
		return nil, fmt.Errorf("不支持的日志格式:%s", config.Format)
	}

	// 创建 core
	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(os.Stdout),
		level,
	)

	// 创建 logger 选项
	opts := []zap.Option{
		zap.AddStacktrace(zapcore.ErrorLevel),
	}

	// 仅在 debug 级别添加调用者信息
	if level == zapcore.DebugLevel {
		opts = append(opts, zap.AddCaller(), zap.AddCallerSkip(1))
	}

	// 创建 logger
	logger := zap.New(core, opts...)

	return &Logger{Logger: logger}, nil
}

// parseLogLevel 解析日志级别字符串
func parseLogLevel(level string) (zapcore.Level, error) {
	switch level {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("不支持的日志级别:%s", level)
	}
}

// With 返回带有给定字段的 logger
func (l *Logger) With(fields ...zapcore.Field) *Logger {
	return &Logger{Logger: l.Logger.With(fields...)}
}

// Named 返回带有给定名称的 logger
func (l *Logger) Named(name string) *Logger {
	return &Logger{Logger: l.Logger.Named(name)}
}

// WithRequestID 返回带有 request ID 字段的 logger
func (l *Logger) WithRequestID(requestID string) *Logger {
	return l.With(zap.String("request_id", requestID))
}

// WithError 返回带有 error 字段的 logger
func (l *Logger) WithError(err error) *Logger {
	return l.With(zap.Error(err))
}
