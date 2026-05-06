package transcription

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/audio"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// TranscriptionManager 管理频率的转写处理器
type TranscriptionManager struct {
	processors           map[string]ProcessorInterface
	mu                   sync.RWMutex
	wsServer             *websocket.Server
	transcriptionStorage *sqlite.TranscriptionStorage
	aircraftStorage      *sqlite.AircraftStorage
	clearanceStorage     *sqlite.ClearanceStorage
	logger               *logger.Logger
	openAIAPIKey         string
	transcriptionConfig  Config
	postProcessor        *PostProcessor
	postProcessingConfig PostProcessingConfig
	templateRenderer     TemplateRenderer
	frequencyNames       map[string]string // 频率 ID 到名称的映射
	fileLogger           *FileLogger       // 转写的可选文件日志记录器
}

// NewTranscriptionManager 创建一个新的转写管理器
func NewTranscriptionManager(
	wsServer *websocket.Server,
	transcriptionStorage *sqlite.TranscriptionStorage,
	aircraftStorage *sqlite.AircraftStorage,
	clearanceStorage *sqlite.ClearanceStorage,
	logger *logger.Logger,
	openAIAPIKey string,
	transcriptionConfig Config,
	postProcessingConfig PostProcessingConfig,
	templateRenderer TemplateRenderer,
	frequencyConfigs []FrequencyConfig,
) *TranscriptionManager {
	// 创建频率 ID 到名称的映射
	frequencyNames := make(map[string]string)
	for _, freq := range frequencyConfigs {
		frequencyNames[freq.ID] = freq.Name
	}

	// 如果配置了 log_dir,则创建文件日志记录器
	var fileLogger *FileLogger
	if transcriptionConfig.LogDir != "" {
		var err error
		fileLogger, err = NewFileLogger(transcriptionConfig.LogDir, logger)
		if err != nil {
			logger.Error("创建转写文件日志记录器失败",
				String("log_dir", transcriptionConfig.LogDir),
				Error(err))
			// 不带文件日志继续运行
			fileLogger = nil
		}
	}

	return &TranscriptionManager{
		processors:           make(map[string]ProcessorInterface),
		wsServer:             wsServer,
		transcriptionStorage: transcriptionStorage,
		aircraftStorage:      aircraftStorage,
		clearanceStorage:     clearanceStorage,
		logger:               logger,
		openAIAPIKey:         openAIAPIKey,
		transcriptionConfig:  transcriptionConfig,
		postProcessingConfig: postProcessingConfig,
		templateRenderer:     templateRenderer,
		frequencyNames:       frequencyNames,
		fileLogger:           fileLogger,
	}
}

// FrequencyConfig 表示频率配置
type FrequencyConfig struct {
	ID   string
	Name string
}

// StartTranscription 启动频率的转写
func (m *TranscriptionManager) StartTranscription(
	ctx context.Context,
	frequencyID string,
	frequencyName string,
	audioURL string,
	transcribeAudio bool,
) error {
	// 如果该频率未启用转写,则跳过
	if !transcribeAudio {
		m.logger.Info("该频率未启用转写",
			logger.String("id", frequencyID),
			logger.String("name", frequencyName))
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查处理器是否已存在
	if _, exists := m.processors[frequencyID]; exists {
		m.logger.Info("该频率的转写已经启动",
			logger.String("id", frequencyID),
			logger.String("name", frequencyName))
		return nil
	}

	m.logger.Info("正在启动频率的转写",
		logger.String("id", frequencyID),
		logger.String("name", frequencyName),
		logger.String("url", audioURL))

	// 为该频率创建一个 CentralAudioProcessor
	audioConfig := audio.CentralProcessorConfig{
		FFmpegPath:               m.transcriptionConfig.FFmpegPath,
		SampleRate:               m.transcriptionConfig.FFmpegSampleRate,
		Channels:                 m.transcriptionConfig.FFmpegChannels,
		Format:                   m.transcriptionConfig.FFmpegFormat,
		ReconnectDelay:           time.Duration(m.transcriptionConfig.ReconnectIntervalSec) * time.Second,
		FFmpegTimeoutSecs:        0, // 转写默认无超时
		FFmpegReconnectDelaySecs: 2, // 转写默认重连延迟
	}

	audioProcessor, err := audio.NewCentralAudioProcessor(
		ctx,
		frequencyID,
		audioURL,
		audioConfig,
		m.logger.Named("audio"),
	)
	if err != nil {
		return fmt.Errorf("创建音频处理器失败: %w", err)
	}

	// 启动音频处理器
	if err := audioProcessor.Start(); err != nil {
		return fmt.Errorf("启动音频处理器失败: %w", err)
	}

	// 为转写创建原始 PCM 读取器(无 WAV 头)
	reader, err := audioProcessor.CreateRawReader(fmt.Sprintf("transcription-%s", frequencyID))
	if err != nil {
		audioProcessor.Stop()
		return fmt.Errorf("创建音频读取器失败: %w", err)
	}

	// 创建使用该读取器的处理器
	processor, err := NewProcessor(
		ctx,
		frequencyID,
		reader,
		m.transcriptionConfig,
		m.wsServer,
		m.transcriptionStorage,
		m.logger,
		m.fileLogger,
	)
	if err != nil {
		return err
	}

	// 启动处理器
	if err := processor.Start(); err != nil {
		return err
	}

	// 存储处理器
	m.processors[frequencyID] = processor

	return nil
}

// StartTranscriptionWithExternalAudio 使用外部音频处理器启动频率的转写
func (m *TranscriptionManager) StartTranscriptionWithExternalAudio(
	ctx context.Context,
	frequencyID string,
	frequencyName string,
	transcribeAudio bool,
	audioProcessor interface{},
) error {
	// 如果该频率未启用转写,则跳过
	if !transcribeAudio {
		m.logger.Info("该频率未启用转写",
			logger.String("id", frequencyID),
			logger.String("name", frequencyName))
		return nil
	}

	// 如果没有提供 OpenAI API 密钥,则跳过
	if m.openAIAPIKey == "" {
		m.logger.Info("转写已禁用 - 未提供 OpenAI API 密钥",
			logger.String("id", frequencyID),
			logger.String("name", frequencyName))
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查处理器是否已存在
	if _, exists := m.processors[frequencyID]; exists {
		m.logger.Info("该频率的转写已经启动",
			logger.String("id", frequencyID),
			logger.String("name", frequencyName))
		return nil
	}

	m.logger.Info("正在使用外部音频启动频率的转写",
		logger.String("id", frequencyID),
		logger.String("name", frequencyName))

	// 根据音频处理器类型创建外部处理器
	var processor ProcessorInterface
	var err error

	// 我们现在仅支持 CentralAudioProcessor
	ap, ok := audioProcessor.(*audio.CentralAudioProcessor)
	if !ok {
		return fmt.Errorf("不支持的音频处理器类型: %T,仅支持 CentralAudioProcessor", audioProcessor)
	}

	// 为转写创建原始 PCM 读取器(无 WAV 头)
	reader, readerErr := ap.CreateRawReader(fmt.Sprintf("transcription-%s", frequencyID))
	if readerErr != nil {
		return fmt.Errorf("从中央处理器创建读取器失败: %w", readerErr)
	}

	// 创建使用该读取器的处理器
	processor, err = NewProcessor(
		ctx,
		frequencyID,
		reader,
		m.transcriptionConfig,
		m.wsServer,
		m.transcriptionStorage,
		m.logger,
		m.fileLogger,
	)
	if err != nil {
		return fmt.Errorf("创建外部处理器失败: %w", err)
	}

	// 启动处理器
	if err := processor.Start(); err != nil {
		return fmt.Errorf("启动外部处理器失败: %w", err)
	}

	// 存储处理器
	m.processors[frequencyID] = processor

	return nil
}

// StopTranscription 停止频率的转写
func (m *TranscriptionManager) StopTranscription(frequencyID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查处理器是否存在
	processor, exists := m.processors[frequencyID]
	if !exists {
		m.logger.Info("未找到该频率的转写处理器", logger.String("id", frequencyID))
		return
	}

	m.logger.Info("正在停止频率的转写", logger.String("id", frequencyID))

	// 停止处理器
	if err := processor.Stop(); err != nil {
		m.logger.Error("停止转写处理器时出错",
			logger.String("id", frequencyID),
			logger.Error(err))
	}

	// 移除处理器
	delete(m.processors, frequencyID)
}

// StopAllTranscriptions 停止所有转写处理器和后处理
func (m *TranscriptionManager) StopAllTranscriptions() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("正在停止所有转写处理器", logger.Int("count", len(m.processors)))

	// 停止所有处理器
	for id, processor := range m.processors {
		if err := processor.Stop(); err != nil {
			m.logger.Error("停止转写处理器时出错",
				logger.String("id", id),
				logger.Error(err))
		}
	}

	// 清除处理器
	m.processors = make(map[string]ProcessorInterface)

	// 停止后处理器
	m.StopPostProcessing()

	// 关闭文件日志记录器
	if m.fileLogger != nil {
		if err := m.fileLogger.Close(); err != nil {
			m.logger.Error("关闭文件日志记录器失败", Error(err))
		}
	}
}

// StartPostProcessing 启动转写的后处理
func (m *TranscriptionManager) StartPostProcessing(ctx context.Context) error {
	if m.postProcessor != nil {
		m.logger.Info("后处理已经启动")
		return nil
	}

	// 如果没有提供 OpenAI API 密钥,则跳过
	if m.openAIAPIKey == "" {
		m.logger.Info("后处理已禁用 - 未提供 OpenAI API 密钥")
		return nil
	}

	// 创建用于后处理的 OpenAI 客户端
	openaiClient := NewOpenAIClient(m.openAIAPIKey, m.postProcessingConfig.Model, m.postProcessingConfig.TimeoutSeconds, m.logger)

	// 创建后处理器
	var err error
	m.postProcessor, err = NewPostProcessor(
		ctx,
		m.transcriptionStorage,
		m.aircraftStorage,
		m.clearanceStorage,
		openaiClient,
		m.wsServer,
		m.templateRenderer,
		m.postProcessingConfig,
		m.logger,
		m.frequencyNames,
		m.fileLogger,
	)
	if err != nil {
		return fmt.Errorf("创建后处理器失败: %w", err)
	}

	// 启动后处理器
	if err := m.postProcessor.Start(); err != nil {
		return fmt.Errorf("启动后处理器失败: %w", err)
	}

	m.logger.Info("后处理已启动")
	return nil
}

// StopPostProcessing 停止转写的后处理
func (m *TranscriptionManager) StopPostProcessing() {
	if m.postProcessor == nil {
		m.logger.Info("没有要停止的后处理器")
		return
	}

	m.logger.Info("正在停止后处理器")
	m.postProcessor.Stop()
	m.postProcessor = nil
}
