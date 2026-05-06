package frequencies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/audio"
	cfg "github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/transcription"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// 导入 logger 包导出的函数
var (
	String = logger.String
	Int    = logger.Int
	Error  = logger.Error
	Bool   = logger.Bool
)

// StreamProcessor 管理可被多个客户端共享的单个频率流。
type StreamProcessor struct {
	id                string
	audioProcessor    *audio.CentralAudioProcessor
	contentType       string
	status            string
	lastActivity      time.Time
	clients           map[string]*ClientStreamReader
	clientsMu         sync.RWMutex
	audioURL          string
	client            *Client
	ctx               context.Context
	cancel            context.CancelFunc
	logger            *logger.Logger
	clientLastActive  map[string]time.Time // 跟踪每个客户端最后活跃的时间
	clientCleanupTick *time.Ticker         // 用于清理不活跃客户端的 ticker
}

// NewStreamProcessor 为某个频率创建一个新的流处理器。
func NewStreamProcessor(
	ctx context.Context,
	id string,
	audioURL string,
	client *Client,
	config *cfg.Config,
	logger *logger.Logger,
) (*StreamProcessor, error) {
	procCtx, procCancel := context.WithCancel(ctx)

	// 创建音频处理器
	audioConfig := audio.CentralProcessorConfig{
		FFmpegPath:               config.Transcription.FFmpegPath,
		SampleRate:               config.Transcription.FFmpegSampleRate,
		Channels:                 config.Transcription.FFmpegChannels,
		Format:                   config.Transcription.FFmpegFormat,
		ReconnectDelay:           time.Duration(config.Frequencies.ReconnectIntervalSecs) * time.Second,
		FFmpegTimeoutSecs:        config.Frequencies.FFmpegTimeoutSecs,
		FFmpegReconnectDelaySecs: config.Frequencies.FFmpegReconnectDelaySecs,
	}

	audioProcessor, err := audio.NewCentralAudioProcessor(
		procCtx,
		id,
		audioURL,
		audioConfig,
		logger.Named("audio"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建音频处理器失败: %w", err)
	}

	sp := &StreamProcessor{
		id:               id,
		audioProcessor:   audioProcessor,
		contentType:      "audio/wav", // 我们现在提供 WAV 格式
		status:           "initializing",
		lastActivity:     time.Now(),
		clients:          make(map[string]*ClientStreamReader),
		clientLastActive: make(map[string]time.Time),
		clientsMu:        sync.RWMutex{},
		audioURL:         audioURL,
		client:           client,
		ctx:              procCtx,
		cancel:           procCancel,
		logger:           logger.Named("freq-stream").With(String("id", id)),
	}

	// 启动一个 ticker,每 10 秒清理一次不活跃的客户端
	sp.clientCleanupTick = time.NewTicker(10 * time.Second)
	go sp.cleanupInactiveClients()

	return sp, nil
}

// cleanupInactiveClients 周期性地检查并移除不活跃的客户端
func (sp *StreamProcessor) cleanupInactiveClients() {
	for {
		select {
		case <-sp.ctx.Done():
			// 处理器停止时停止清理
			if sp.clientCleanupTick != nil {
				sp.clientCleanupTick.Stop()
			}
			return
		case <-sp.clientCleanupTick.C:
			sp.removeInactiveClients()
		}
	}
}

// removeInactiveClients 移除超过 30 秒未活跃的客户端
func (sp *StreamProcessor) removeInactiveClients() {
	sp.clientsMu.Lock()
	defer sp.clientsMu.Unlock()

	now := time.Now()
	inactiveThreshold := 30 * time.Second
	inactiveClients := []string{}

	// 记录当前客户端状态以便调试
	if len(sp.clients) > 0 {
		sp.logger.Debug("客户端活跃度检查",
			Int("total_clients", len(sp.clients)),
			String("threshold", inactiveThreshold.String()))
	}

	// 查找不活跃的客户端
	for clientID, lastActive := range sp.clientLastActive {
		inactiveDuration := now.Sub(lastActive)
		if inactiveDuration > inactiveThreshold {
			inactiveClients = append(inactiveClients, clientID)
			sp.logger.Warn("客户端因超时被标记为不活跃",
				String("clientID", clientID),
				String("inactive_duration", inactiveDuration.String()),
				String("threshold", inactiveThreshold.String()))
		}
	}

	// 同时检查 reader 已关闭但仍在 map 中的客户端
	for clientID, reader := range sp.clients {
		if reader != nil {
			reader.mu.Lock()
			isClosed := reader.closed
			reader.mu.Unlock()

			if isClosed {
				// reader 已关闭但仍在 map 中,加入清理列表
				if !contains(inactiveClients, clientID) {
					inactiveClients = append(inactiveClients, clientID)
					sp.logger.Warn("发现已关闭但仍在 map 中的客户端 reader,将其标记为待清理",
						String("clientID", clientID))
				}
			}
		}
	}

	// 移除不活跃的客户端
	for _, clientID := range inactiveClients {
		lastActive, hasLastActive := sp.clientLastActive[clientID]
		var inactiveDuration time.Duration
		if hasLastActive {
			inactiveDuration = now.Sub(lastActive)
		}

		sp.logger.Warn("正在移除不活跃的客户端",
			String("clientID", clientID),
			String("inactive_duration", inactiveDuration.String()),
			Bool("had_last_active", hasLastActive))

		if reader, exists := sp.clients[clientID]; exists {
			reader.Close()
			delete(sp.clients, clientID)
		}
		delete(sp.clientLastActive, clientID)
	}

	if len(inactiveClients) > 0 {
		sp.logger.Warn("已移除不活跃的客户端",
			Int("count", len(inactiveClients)),
			Int("remaining", len(sp.clients)))
	}
}

// 辅助函数,检查切片是否包含某个字符串
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// Start 开始处理音频流。
func (sp *StreamProcessor) Start() error {
	sp.logger.Info("正在启动流处理器")

	// 启动音频处理器
	if err := sp.audioProcessor.Start(); err != nil {
		return fmt.Errorf("启动音频处理器失败: %w", err)
	}

	sp.status = "streaming"
	sp.lastActivity = time.Now()

	// 转写将由 Service.Start 方法
	// 根据实际的 TranscribeAudio 配置值来启动

	return nil
}

// processStream 方法已被移除,因为不再需要
// 音频处理器现在已处理所有的流式传输功能

// Stop 停止流处理器并清理资源。
func (sp *StreamProcessor) Stop() {
	sp.logger.Info("正在停止流处理器")

	// 取消 context 以停止所有操作
	sp.cancel()

	// 停止客户端清理 ticker
	if sp.clientCleanupTick != nil {
		sp.clientCleanupTick.Stop()
	}

	// 立即强制关闭所有客户端连接
	sp.clientsMu.Lock()
	// 首先,对所有 reader 调用 Close,这会取消它们的 context。
	for clientID, reader := range sp.clients {
		sp.logger.Info("关机过程中正在关闭客户端连接",
			String("clientID", clientID))
		if reader != nil {
			reader.Close() // 这是新的 ClientStreamReader.Close(),不再调用 RemoveClient。
		}
	}
	// 现在所有 reader 都已被标记为关闭,且其 context 已被取消,
	// 清空两个 map。
	sp.clients = make(map[string]*ClientStreamReader)
	sp.clientLastActive = make(map[string]time.Time)
	sp.clientsMu.Unlock()

	// 停止音频处理器
	if sp.audioProcessor != nil {
		if err := sp.audioProcessor.Stop(); err != nil {
			sp.logger.Error("停止音频处理器时出错", Error(err))
		}
	}

	sp.logger.Info("流处理器已停止")
}

// AddClient 向流处理器添加新的客户端。
func (sp *StreamProcessor) AddClient(clientID string) *ClientStreamReader {
	sp.clientsMu.Lock()
	defer sp.clientsMu.Unlock()

	// 检查客户端是否已存在
	if existingReader, exists := sp.clients[clientID]; exists {
		// 检查现有 reader 是否实际已关闭。
		// 访问 existingReader.closed 需要其自身的互斥锁(若频繁竞争),
		// 但这里我们持有 sp.clientsMu,提供了一定保护。明确检查更安全。
		existingReader.mu.Lock() // 锁定具体 reader 以检查其关闭状态
		isClosed := existingReader.closed
		existingReader.mu.Unlock()

		if !isClosed {
			sp.logger.Info("客户端已连接且活跃,正在更新最后活跃时间",
				String("clientID", clientID))
			// 更新最后活跃时间
			sp.clientLastActive[clientID] = time.Now()
			return existingReader // 返回活跃的现有 reader
		} else {
			sp.logger.Info("发现已存在但已关闭的客户端 reader,在创建新 reader 之前先移除",
				String("clientID", clientID))
			// reader.Close() 应该已经取消了它的 context。
			// 这里我们只需将其从 map 中移除。
			delete(sp.clients, clientID)
			delete(sp.clientLastActive, clientID)
			// 继续为该 clientID 创建新的 reader
		}
	}

	sp.logger.Info("正在添加新客户端(或替换已关闭的客户端)", String("clientID", clientID))

	// 从音频处理器创建一个 reader
	audioReader, err := sp.audioProcessor.CreateReader(clientID)
	if err != nil {
		sp.logger.Error("创建音频 reader 失败", Error(err), String("clientID", clientID))
		// 返回一个会立即返回 EOF 的占位 reader
		return &ClientStreamReader{
			ReadCloser:   io.NopCloser(strings.NewReader("")),
			logger:       sp.logger.Named("client-stream-reader"),
			streamID:     sp.id,
			once:         sync.Once{},
			processor:    sp,
			clientID:     clientID,
			lastActivity: time.Now(),
			ctx:          context.Background(),
			cancel:       func() {},
			closed:       true,
		}
	}

	// 创建带 processor 和 clientID 的 NonClosingReader
	nonClosingReader := &NonClosingReader{
		ReadCloser: audioReader,
		processor:  sp,
		clientID:   clientID,
	}

	// 为该客户端创建带 cancel 的 context
	ctx, cancel := context.WithCancel(sp.ctx)

	// 创建一个新的 reader,从音频处理器读取数据
	reader := &ClientStreamReader{
		ReadCloser:   nonClosingReader,
		logger:       sp.logger.Named("client-stream-reader"),
		streamID:     sp.id,
		once:         sync.Once{},
		processor:    sp,
		clientID:     clientID,
		lastActivity: time.Now(),
		ctx:          ctx,
		cancel:       cancel,
	}

	// 存储客户端并跟踪活跃时间
	sp.clients[clientID] = reader
	sp.clientLastActive[clientID] = time.Now()

	// 记录当前客户端数量
	sp.logger.Info("客户端已添加",
		String("clientID", clientID),
		Int("total_clients", len(sp.clients)))

	return reader
}

// IsClientConnected 在不影响连接的情况下检查客户端是否已连接
func (sp *StreamProcessor) IsClientConnected(clientID string) bool {
	sp.clientsMu.RLock()
	defer sp.clientsMu.RUnlock()

	if existingReader, exists := sp.clients[clientID]; exists {
		existingReader.mu.Lock()
		isClosed := existingReader.closed
		existingReader.mu.Unlock()
		return !isClosed
	}
	return false
}

// RemoveClient 从流处理器中移除一个客户端。
func (sp *StreamProcessor) RemoveClient(clientID string) {
	sp.clientsMu.Lock()
	defer sp.clientsMu.Unlock()

	if reader, exists := sp.clients[clientID]; exists {
		sp.logger.Info("正在移除客户端", String("clientID", clientID))
		reader.Close()
		delete(sp.clients, clientID)
		delete(sp.clientLastActive, clientID)

		// 记录当前客户端数量
		sp.logger.Info("客户端已移除",
			String("clientID", clientID),
			Int("remaining_clients", len(sp.clients)))
	}
}

// GetClientCount 返回已连接的客户端数量。
func (sp *StreamProcessor) GetClientCount() int {
	sp.clientsMu.RLock()
	defer sp.clientsMu.RUnlock()
	return len(sp.clients)
}

// NonClosingReader 包装一个 ReadCloser,但阻止 Close() 影响底层 reader。
// 它在调用 Read 时也会更新客户端的最后活跃时间。
type NonClosingReader struct {
	io.ReadCloser
	processor *StreamProcessor
	clientID  string
}

// NewNonClosingReader 创建一个新的 NonClosingReader。
func NewNonClosingReader(r io.ReadCloser) *NonClosingReader {
	return &NonClosingReader{
		ReadCloser: r,
		// processor 和 clientID 将由 StreamProcessor.AddClient 方法设置
	}
}

// Read 读取数据并更新最后活跃时间
func (ncr *NonClosingReader) Read(p []byte) (n int, err error) {
	n, err = ncr.ReadCloser.Read(p)

	// 如果设置了 processor 和 clientID,则更新最后活跃时间
	if ncr.processor != nil && ncr.clientID != "" && n > 0 {
		ncr.processor.updateClientActivity(ncr.clientID)
	}

	return n, err
}

// Close 是一个空操作,以防止关闭底层 reader。
func (ncr *NonClosingReader) Close() error {
	// 这里有意是个空操作,以防止关闭共享缓冲区
	return nil
}

// updateClientActivity 更新某个客户端的最后活跃时间
func (sp *StreamProcessor) updateClientActivity(clientID string) {
	now := time.Now()

	// 首先尝试用读锁来检查是否需要更新。
	sp.clientsMu.RLock()
	lastActive, exists := sp.clientLastActive[clientID]
	needsUpdate := false
	if exists && now.Sub(lastActive) >= 5*time.Second {
		needsUpdate = true
	}
	sp.clientsMu.RUnlock() // 释放读锁

	// 如果在持有读锁时检查发现不需要更新,则提前返回。
	if !needsUpdate {
		return
	}

	// 如果可能需要更新,获取完整的写锁。
	sp.clientsMu.Lock()
	defer sp.clientsMu.Unlock() // 确保返回时释放写锁

	// 在写锁下重新检查条件,因为在释放 RLock 与获取 WLock 之间状态可能已改变,
	// 或者客户端可能已被移除。
	// 同时,在更新前确保客户端仍存在于 map 中。
	if currentLastActive, stillExists := sp.clientLastActive[clientID]; stillExists {
		// 仅在条件(已过 5 秒)仍然成立时更新。
		// 这处理了另一个 goroutine 已更新它,
		// 或客户端在 RUnlock 与 Lock 之间的小窗口内被重新添加的情况。
		if now.Sub(currentLastActive) >= 5*time.Second {
			sp.clientLastActive[clientID] = now
		}
	}
	// 如果客户端被另一个例程移除或更新,我们这里什么都不做,
	// 这是安全的。
}

// Service 通过持久连接管理频率音频流。
type Service struct {
	client               *Client
	frequenciesConfig    map[string]*cfg.FrequencyConfig
	bufferSize           int
	config               *cfg.Config
	logger               *logger.Logger
	activeStreams        map[string]*StreamProcessor
	streamsMu            sync.RWMutex
	ctx                  context.Context
	cancel               context.CancelFunc
	streamPortIndex      int   // 用于轮询端口选择
	allServerPorts       []int // 主端口与附加端口的合并列表
	transcriptionManager *transcription.TranscriptionManager
	wsServer             *websocket.Server // 用于广播状态更新的 WebSocket 服务器
	connectionStatus     map[string]connectionStatusInfo // 跟踪每个频率的连接状态
	statusMu             sync.RWMutex                    // 用于 connectionStatus map 的互斥锁
}

// connectionStatusInfo 存储某个频率当前的连接状态和错误
type connectionStatusInfo struct {
	Status    string
	Error     string
	UpdatedAt time.Time
}

// NewService 创建一个新的频率服务。
func NewService(
	config *cfg.Config,
	logger *logger.Logger,
	wsServer *websocket.Server,
	transcriptionStorage *sqlite.TranscriptionStorage,
	aircraftStorage *sqlite.AircraftStorage,
	clearanceStorage *sqlite.ClearanceStorage,
	templateRenderer transcription.TemplateRenderer,
) *Service {
	// 实验:减小缓冲区大小,以观察对感知"实时"延迟的影响
	bufferSize := 4 * 1024 // 4KB 缓冲,在 16kbps 下大约为 2 秒
	if config.Frequencies.BufferSizeKB > 0 {
		bufferSize = config.Frequencies.BufferSizeKB * 1024
	}

	freqsConfig := make(map[string]*cfg.FrequencyConfig)
	for i := range config.Frequencies.Sources {
		src := config.Frequencies.Sources[i]
		freqsConfig[src.ID] = &src
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 创建转写管理器
	transcriptionConfig := transcription.Config{
		OpenAIAPIKey:          config.Transcription.OpenAIAPIKey,
		Model:                 config.Transcription.Model,
		Language:              config.Transcription.Language,
		NoiseReduction:        config.Transcription.NoiseReduction,
		ChunkMs:               config.Transcription.ChunkMs,
		BufferSizeKB:          config.Transcription.BufferSizeKB,
		FFmpegPath:            config.Transcription.FFmpegPath,
		FFmpegSampleRate:      config.Transcription.FFmpegSampleRate,
		FFmpegChannels:        config.Transcription.FFmpegChannels,
		FFmpegFormat:          config.Transcription.FFmpegFormat,
		ReconnectIntervalSec:  config.Transcription.ReconnectIntervalSec,
		MaxRetries:            config.Transcription.MaxRetries,
		TurnDetectionType:     config.Transcription.TurnDetectionType,
		PrefixPaddingMs:       config.Transcription.PrefixPaddingMs,
		SilenceDurationMs:     config.Transcription.SilenceDurationMs,
		VADThreshold:          config.Transcription.VADThreshold,
		RetryMaxAttempts:      config.Transcription.RetryMaxAttempts,
		RetryInitialBackoffMs: config.Transcription.RetryInitialBackoffMs,
		RetryMaxBackoffMs:     config.Transcription.RetryMaxBackoffMs,
		PromptPath:            config.Transcription.PromptPath,
		TimeoutSeconds:        config.Transcription.TimeoutSeconds,
		LogDir:                config.Transcription.LogDir,
	}

	// 从文件加载提示词
	promptBytes, err := os.ReadFile(config.Transcription.PromptPath)
	if err != nil {
		logger.Error("读取转写提示词文件失败,将使用空提示词",
			Error(err),
			String("path", config.Transcription.PromptPath))
		transcriptionConfig.Prompt = ""
	} else {
		transcriptionConfig.Prompt = string(promptBytes)
		logger.Info("已从文件加载转写提示词",
			String("path", config.Transcription.PromptPath),
			Int("prompt_length", len(transcriptionConfig.Prompt)))
	}

	postProcessingConfig := transcription.PostProcessingConfig{
		Enabled:               config.PostProcessing.Enabled,
		Model:                 config.PostProcessing.Model,
		IntervalSeconds:       config.PostProcessing.IntervalSeconds,
		BatchSize:             config.PostProcessing.BatchSize,
		ContextTranscriptions: config.PostProcessing.ContextTranscriptions,
		SystemPromptPath:      config.PostProcessing.SystemPromptPath,
		TimeoutSeconds:        config.PostProcessing.TimeoutSeconds,
	}

	// 将频率配置转换为 TranscriptionManager 期望的格式
	var frequencyConfigs []transcription.FrequencyConfig
	for _, freq := range config.Frequencies.Sources {
		frequencyConfigs = append(frequencyConfigs, transcription.FrequencyConfig{
			ID:   freq.ID,
			Name: freq.Name,
		})
	}

	transcriptionManager := transcription.NewTranscriptionManager(
		wsServer,
		transcriptionStorage,
		aircraftStorage,
		clearanceStorage,
		logger.Named("transcribe"),
		config.Transcription.OpenAIAPIKey,
		transcriptionConfig,
		postProcessingConfig,
		templateRenderer,
		frequencyConfigs,
	)

	// 准备所有可用服务器端口的列表,用于轮询生成流 URL
	allPorts := []int{config.Server.Port}
	if len(config.Server.AdditionalPorts) > 0 {
		allPorts = append(allPorts, config.Server.AdditionalPorts...)
	}

	return &Service{
		client:               NewClient(0, logger),
		frequenciesConfig:    freqsConfig,
		bufferSize:           bufferSize,
		config:               config,
		logger:               logger.Named("freq-service"),
		activeStreams:        make(map[string]*StreamProcessor),
		streamsMu:            sync.RWMutex{},
		ctx:                  ctx,
		cancel:               cancel,
		streamPortIndex:      0,
		allServerPorts:       allPorts,
		transcriptionManager: transcriptionManager,
		wsServer:             wsServer,
		connectionStatus:     make(map[string]connectionStatusInfo),
	}
}

// Start 初始化与所有已配置频率的连接。
func (s *Service) Start(ctx context.Context) error {
	s.logger.Info("正在启动频率服务并建立持久连接")

	// 为每个已配置的频率启动一个流处理器
	for id, freqConfig := range s.frequenciesConfig {
		s.logger.Info("正在为频率启动流处理器",
			String("id", id),
			String("name", freqConfig.Name),
			String("url", freqConfig.URL))

		processor, err := NewStreamProcessor(
			s.ctx,
			id,
			freqConfig.URL,
			s.client,
			s.config,
			s.logger,
		)

		if err != nil {
			s.logger.Error("创建流处理器失败",
				String("id", id),
				Error(err))
			// 广播失败状态
			s.broadcastFrequencyStatus(id, audio.StatusFailed, err.Error())
			continue
		}

		// 设置状态回调,通过 WebSocket 广播状态变化
		processor.audioProcessor.SetStatusCallback(s.broadcastFrequencyStatus)

		err = processor.Start()
		if err != nil {
			s.logger.Error("启动流处理器失败",
				String("id", id),
				Error(err))
			// 广播失败状态
			s.broadcastFrequencyStatus(id, audio.StatusFailed, err.Error())
			continue
		}

		s.streamsMu.Lock()
		s.activeStreams[id] = processor
		s.streamsMu.Unlock()

		// 如果启用,使用外部音频启动转写
		frequency := &Frequency{
			ID:              id,
			Name:            freqConfig.Name,
			URL:             freqConfig.URL,
			TranscribeAudio: freqConfig.TranscribeAudio,
		}

		if frequency.TranscribeAudio {
			s.logger.Info("正在为频率使用外部音频启动转写",
				String("id", id),
				String("name", freqConfig.Name),
				Bool("transcribe_audio", freqConfig.TranscribeAudio))

			if err := s.transcriptionManager.StartTranscriptionWithExternalAudio(
				s.ctx,
				frequency.ID,
				frequency.Name,
				frequency.TranscribeAudio,
				processor.audioProcessor,
			); err != nil {
				s.logger.Error("为频率使用外部音频启动转写失败",
					String("id", id),
					Error(err))
			}
		} else {
			s.logger.Info("该频率未启用转写",
				String("id", id),
				String("name", freqConfig.Name),
				Bool("transcribe_audio", freqConfig.TranscribeAudio))
		}
	}

	s.logger.Info("所有频率流处理器均已启动")

	// 如果启用,启动后处理
	if s.config.PostProcessing.Enabled {
		s.logger.Info("正在启动后处理")
		if err := s.transcriptionManager.StartPostProcessing(s.ctx); err != nil {
			s.logger.Error("启动后处理失败", Error(err))
			// 即使后处理失败也继续运行
		}
	} else {
		s.logger.Info("后处理已禁用")
	}

	return nil
}

// Stop 停止所有流处理器并清理资源。
func (s *Service) Stop() {
	s.logger.Info("频率服务正在停止")

	// 停止所有转写
	s.transcriptionManager.StopAllTranscriptions()

	// 取消主 context,通知所有流处理器停止
	s.cancel()

	// 创建 WaitGroup 等待所有处理器停止
	var wg sync.WaitGroup

	// 停止每个流处理器
	s.streamsMu.Lock()
	for id, processor := range s.activeStreams {
		if processor != nil {
			wg.Add(1)
			go func(id string, proc *StreamProcessor) {
				defer wg.Done()
				s.logger.Info("正在停止流处理器", String("id", id))
				proc.Stop()
			}(id, processor)
		}
	}
	s.streamsMu.Unlock()

	// 带超时地等待所有处理器停止
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("所有流处理器均已停止")
	case <-time.After(5 * time.Second):
		s.logger.Warn("等待流处理器停止超时")
	}

	// 清空活跃流的 map
	s.streamsMu.Lock()
	s.activeStreams = make(map[string]*StreamProcessor)
	s.streamsMu.Unlock()

	s.logger.Info("频率服务已停止")
}

// ClientStreamReader 管理单个客户端音频流资源的生命周期。
// 它的 Close 方法对清理至关重要。
type ClientStreamReader struct {
	io.ReadCloser // 客户端的专用循环缓冲区
	logger        *logger.Logger
	streamID      string
	once          sync.Once // 确保清理动作仅执行一次
	processor     *StreamProcessor
	clientID      string
	lastActivity  time.Time          // 跟踪该客户端的最后活跃时间
	ctx           context.Context    // 用于取消的 context
	cancel        context.CancelFunc // 用于取消 context 的函数
	closed        bool               // 标记 reader 是否已关闭
	mu            sync.Mutex         // 保护 closed 标志的互斥锁
}

// Close 清理该客户端流的资源。
func (csr *ClientStreamReader) Close() error {
	var err error // 用于存储 ReadCloser.Close 的可能错误
	csr.once.Do(func() {
		csr.mu.Lock()
		if csr.closed {
			csr.mu.Unlock()
			return // 已被另一 goroutine 关闭
		}
		csr.closed = true
		csr.mu.Unlock()

		csr.logger.Info("正在关闭客户端流 reader,取消 context 并清理本地资源",
			String("streamID", csr.streamID),
			String("clientID", csr.clientID))

		// 取消 context,通知所有使用此 reader 的操作停止
		if csr.cancel != nil {
			csr.cancel()
		}

		// 从 StreamProcessor 的 map 中移除该客户端的责任
		// 现在归属于发起关闭的调用方(例如 removeInactiveClients、
		// 或在 io.Copy 之后的 HTTP handler、或 StreamProcessor.Stop)。
		// 切勿在此处调用:csr.processor.RemoveClient(csr.clientID),以避免死锁。

		// 如果底层 reader 是该客户端独有的实际资源,则关闭它。
		// 对于 NonClosingReader,ReadCloser.Close() 是空操作。
		if csr.ReadCloser != nil {
			internalErr := csr.ReadCloser.Close()
			if internalErr != nil {
				// 存储关闭过程中遇到的第一个错误。
				// 由于 NonClosingReader.Close() 是空操作并返回 nil,该 'err' 通常仍会是 nil。
				err = internalErr
				csr.logger.Error("关闭 ClientStreamReader 中底层 ReadCloser 时出错",
					String("streamID", csr.streamID),
					String("clientID", csr.clientID),
					Error(internalErr))
			}
		}
	})
	return err
}

// Read 从底层 reader 读取数据,并处理超时
func (csr *ClientStreamReader) Read(p []byte) (n int, err error) {
	// 检查是否已关闭
	csr.mu.Lock()
	if csr.closed {
		csr.mu.Unlock()
		return 0, io.EOF
	}
	csr.mu.Unlock()

	// 注意:此处不再更新 csr.lastActivity。
	// 用于处理器清理的关键最后活跃时间由 NonClosingReader.Read
	// 调用 processor.updateClientActivity 来更新。

	// 在尝试读取前检查 context 是否已取消
	select {
	case <-csr.ctx.Done():
		// 该客户端流的 context 已被取消。
		// 这可能是因为 HTTP 请求结束,或 StreamProcessor 停止了该客户端。
		// 确保调用 Close(它是幂等的)以将 csr.closed 标记为 true。
		csr.Close()
		return 0, io.EOF
	default:
		// context 未结束,继续从底层源读取。
	}

	// csr.ReadCloser 是 NonClosingReader,封装了共享的 CircularBuffer。
	// CircularBuffer.Read 会阻塞,直到有数据可用或缓冲区本身被关闭。
	n, err = csr.ReadCloser.Read(p)

	// 处理读取操作的结果
	if err != nil {
		// 如果出现错误,包括 io.EOF(若 CircularBuffer 已关闭),
		// 我们应确保该 ClientStreamReader 也被标记为关闭。
		// csr.Close() 是幂等的,可以处理这种情况。
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
			csr.logger.Info("底层 reader 返回 EOF 或管道已关闭,正在关闭客户端流 reader",
				String("streamID", csr.streamID), String("clientID", csr.clientID))
			csr.Close()
		} else {
			csr.logger.Error("从底层 ReadCloser 读取时出错",
				String("streamID", csr.streamID), String("clientID", csr.clientID), Error(err))
			// 对于其他错误,也确保此客户端 reader 关闭以防止后续问题。
			csr.Close()
		}
		return n, err // 传播原始错误(n 在出现错误时也可能 >0)
	}

	// 如果 n == 0 且 err == nil:
	// 当前的 CircularBuffer.Read 设计为会阻塞,直到有数据可用或被关闭
	// (返回 n>0 或 io.EOF)。它不应返回 (0, nil)。
	// 如果以某种方式发生了,返回 (0, nil) 通常对 io.Copy 是可接受的,它会重试。
	// 此处不需要对该理论情况做特殊处理;只需返回收到的内容。
	return n, nil
}

// GetAudioStream 返回某频率音频流的 reader。
// 它接受一个客户端 ID,以跟踪单个客户端的连接。
func (s *Service) GetAudioStream(ctx context.Context, id string, clientID string) (io.ReadCloser, string, error) {
	// 创建带超时的 context,以防止挂起
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// 检查频率是否存在
	freqConfig, ok := s.frequenciesConfig[id]
	if !ok {
		return nil, "", fmt.Errorf("未找到频率配置: %s", id)
	}

	s.logger.Info("客户端正在请求音频流",
		String("id", id),
		String("clientID", clientID))

	// 检查该客户端是否已连接到该频率
	s.streamsMu.RLock()
	processor, exists := s.activeStreams[id]
	s.streamsMu.RUnlock()

	if exists && processor.IsClientConnected(clientID) {
		s.logger.Info("客户端已连接到该频率,拒绝重复请求",
			String("id", id),
			String("clientID", clientID))
		return nil, "", fmt.Errorf("客户端已连接到该频率")
	}

	// 检查是否已达到活跃客户端的最大数量
	s.streamsMu.RLock()
	totalClients := 0
	for _, proc := range s.activeStreams {
		totalClients += proc.GetClientCount()
	}
	s.streamsMu.RUnlock()

	// 总并发客户端限制为 20,每个频率限制为 5,以防止资源耗尽
	if totalClients > 100 {
		s.logger.Warn("并发客户端过多,拒绝连接",
			String("id", id),
			String("clientID", clientID),
			Int("total_clients", totalClients))
		return nil, "", fmt.Errorf("并发客户端过多(最多 100)")
	}

	// 检查是否已有该频率的处理器
	s.streamsMu.RLock()
	processor, exists = s.activeStreams[id]
	s.streamsMu.RUnlock()

	// 如果处理器已存在,则检查该具体频率的客户端数量
	if exists && processor.GetClientCount() >= 10 {
		s.logger.Warn("该频率的客户端过多,拒绝连接",
			String("id", id),
			String("clientID", clientID),
			Int("client_count", processor.GetClientCount()))
		return nil, "", fmt.Errorf("该频率的客户端过多(最多 10)")
	}

	// 我们已经在上面的检查中获得了处理器,无需再次获取

	if !exists {
		s.logger.Info("未找到流处理器,正在创建新的", String("id", id))

		s.streamsMu.Lock()
		// 在等待锁时,另一 goroutine 可能已经创建了它,因此再次检查
		processor, exists = s.activeStreams[id]
		if !exists {
			var err error
			processor, err = NewStreamProcessor(
				s.ctx,
				id,
				freqConfig.URL,
				s.client,
				s.config,
				s.logger,
			)

			if err != nil {
				s.streamsMu.Unlock()
				s.logger.Error("创建流处理器失败", String("id", id), Error(err))
				return nil, "", fmt.Errorf("创建流处理器失败: %w", err)
			}

			err = processor.Start()
			if err != nil {
				s.streamsMu.Unlock()
				s.logger.Error("启动流处理器失败", String("id", id), Error(err))
				return nil, "", fmt.Errorf("启动流处理器失败: %w", err)
			}

			s.activeStreams[id] = processor
		}
		s.streamsMu.Unlock()
	}

	// 检查 context 是否已被取消
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	default:
		// 继续
	}

	// 将客户端添加到流处理器
	clientReader := processor.AddClient(clientID)

	s.logger.Debug("客户端已连接到音频流",
		String("id", id),
		String("clientID", clientID),
		String("contentType", processor.contentType))

	return clientReader, processor.contentType, nil
}

// GetAllFrequencies 和 GetFrequencyByID 现在仅报告已配置的频率,
// 因为"活跃"状态是按客户端的,且不再以同样方式集中跟踪。
// 我们可以基于配置是否存在来表示一个通用的"available"状态。
func (s *Service) GetAllFrequencies() []*Frequency { // 来自 models.go 的 frequencies.Frequency
	// 不需要 RLock,因为 NewService 之后 s.frequenciesConfig 是只读的
	var result []*Frequency

	// 获取连接状态的快照
	s.statusMu.RLock()
	statusSnapshot := make(map[string]connectionStatusInfo)
	for k, v := range s.connectionStatus {
		statusSnapshot[k] = v
	}
	s.statusMu.RUnlock()

	for _, fc := range s.frequenciesConfig {
		streamURL, streamPort := s.buildStreamInfo(fc.ID)

		// 如果可用,获取连接状态
		status := "available"
		lastError := ""
		if statusInfo, exists := statusSnapshot[fc.ID]; exists {
			status = statusInfo.Status
			lastError = statusInfo.Error
		}

		result = append(result, &Frequency{
			ID:              fc.ID,
			Airport:         fc.Airport,
			Name:            fc.Name,
			FrequencyMHz:    fc.FrequencyMHz,
			URL:             fc.URL,
			StreamURL:       streamURL,
			StreamPort:      streamPort,
			Status:          status,
			LastError:       lastError,
			Order:           fc.Order,
			TranscribeAudio: fc.TranscribeAudio,
		})
	}

	// 按 order 排序频率,而不是按 name
	sort.Slice(result, func(i, j int) bool {
		return result[i].Order < result[j].Order
	})

	return result
}

func (s *Service) GetFrequencyByID(id string) (*Frequency, bool) {
	fc, ok := s.frequenciesConfig[id]
	if !ok {
		return nil, false
	}
	streamURL, streamPort := s.buildStreamInfo(fc.ID)

	// 如果可用,获取连接状态
	status := "available"
	lastError := ""
	s.statusMu.RLock()
	if statusInfo, exists := s.connectionStatus[fc.ID]; exists {
		status = statusInfo.Status
		lastError = statusInfo.Error
	}
	s.statusMu.RUnlock()

	return &Frequency{
		ID:              fc.ID,
		Airport:         fc.Airport,
		Name:            fc.Name,
		Order:           fc.Order,
		FrequencyMHz:    fc.FrequencyMHz,
		URL:             fc.URL,
		StreamURL:       streamURL,
		StreamPort:      streamPort,
		Status:          status,
		LastError:       lastError,
		TranscribeAudio: fc.TranscribeAudio,
	}, true
}

// buildStreamInfo 返回流 URL 路径和用于该流的端口。
// 端口通过轮询从所有可用的服务器端口中选择。
func (s *Service) buildStreamInfo(frequencyID string) (string, int) {
	// 通过轮询获取下一个端口
	port := s.allServerPorts[s.streamPortIndex]
	s.streamPortIndex = (s.streamPortIndex + 1) % len(s.allServerPorts)

	// 返回相对 URL 路径,使浏览器使用正确的主机名
	return fmt.Sprintf("/api/v1/stream/%s", frequencyID), port
}

// broadcastFrequencyStatus 将频率状态变化发送给所有已连接的 WebSocket 客户端,
// 并存储该状态以便稍后通过 API 获取
func (s *Service) broadcastFrequencyStatus(frequencyID string, status audio.ConnectionStatus, errorMsg string) {
	// 始终存储状态,即使 WebSocket 服务器不可用
	s.statusMu.Lock()
	s.connectionStatus[frequencyID] = connectionStatusInfo{
		Status:    string(status),
		Error:     errorMsg,
		UpdatedAt: time.Now(),
	}
	s.statusMu.Unlock()

	s.logger.Debug("频率状态已更新",
		String("frequency_id", frequencyID),
		String("status", string(status)),
		String("error", errorMsg))

	// 如果可用,通过 WebSocket 广播
	if s.wsServer == nil {
		return
	}

	message := &websocket.Message{
		Type: websocket.MessageTypeFrequencyStatus,
		Data: map[string]interface{}{
			"frequency_id": frequencyID,
			"status":       string(status),
			"error":        errorMsg,
		},
	}

	s.wsServer.Broadcast(message)
}

// 如果需要动态更新可用频率,可以实现 AddFrequency 和 RemoveFrequency
// 来修改 s.frequenciesConfig。目前假设静态配置。
// 若要使其并发安全,需要使用 s.mu 来保护 s.frequenciesConfig。
