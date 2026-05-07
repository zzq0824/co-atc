package transcription

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// PostProcessingConfig 表示后处理的配置
type PostProcessingConfig struct {
	Enabled               bool
	Model                 string
	IntervalSeconds       int
	BatchSize             int
	ContextTranscriptions int
	SystemPromptPath      string
	TimeoutSeconds        int
}

// PostProcessingResult 表示来自 LLM 的结构化结果
type PostProcessingResult struct {
	ProcessedContent string                      `json:"processed_content"`
	SpeakerType      string                      `json:"speaker_type,omitempty"`
	Callsign         string                      `json:"callsign,omitempty"`
	Clearances       []sqlite.ExtractedClearance `json:"clearances,omitempty"`
}

// TemplateRenderer 是用于使用空域数据渲染模板的接口
type TemplateRenderer interface {
	RenderPostProcessorTemplate(templatePath string) (string, error)
}

// PostProcessor 管理转写的后处理
type PostProcessor struct {
	ctx                  context.Context
	cancel               context.CancelFunc
	transcriptionStorage *sqlite.TranscriptionStorage
	aircraftStorage      *sqlite.AircraftStorage
	clearanceStorage     *sqlite.ClearanceStorage
	openaiClient         *OpenAIClient
	wsServer             *websocket.Server
	templateRenderer     TemplateRenderer
	logger               *logger.Logger
	config               PostProcessingConfig
	processingInterval   time.Duration
	batchSize            int
	wg                   sync.WaitGroup
	frequencyNames       map[string]string // 频率 ID 到名称的映射
	fileLogger           *FileLogger       // 转写的可选文件日志记录器
}

// NewPostProcessor 创建一个新的后处理器
func NewPostProcessor(
	ctx context.Context,
	transcriptionStorage *sqlite.TranscriptionStorage,
	aircraftStorage *sqlite.AircraftStorage,
	clearanceStorage *sqlite.ClearanceStorage,
	openaiClient *OpenAIClient,
	wsServer *websocket.Server,
	templateRenderer TemplateRenderer,
	config PostProcessingConfig,
	logger *logger.Logger,
	frequencyNames map[string]string,
	fileLogger *FileLogger,
) (*PostProcessor, error) {
	// 创建带取消的上下文
	procCtx, procCancel := context.WithCancel(ctx)

	// 创建后处理器
	processor := &PostProcessor{
		ctx:                  procCtx,
		cancel:               procCancel,
		transcriptionStorage: transcriptionStorage,
		aircraftStorage:      aircraftStorage,
		clearanceStorage:     clearanceStorage,
		openaiClient:         openaiClient,
		wsServer:             wsServer,
		templateRenderer:     templateRenderer,
		logger:               logger.Named("post-processor"),
		config:               config,
		processingInterval:   time.Duration(config.IntervalSeconds) * time.Second,
		batchSize:            config.BatchSize,
		frequencyNames:       frequencyNames,
		fileLogger:           fileLogger,
	}

	return processor, nil
}

// Start 启动后处理循环
func (p *PostProcessor) Start() error {
	if !p.config.Enabled {
		p.logger.Info("后处理已禁用,不启动")
		return nil
	}

	p.logger.Info("正在启动后处理循环",
		logger.Int("interval_seconds", p.config.IntervalSeconds),
		logger.Int("batch_size", p.batchSize))

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.processingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				p.logger.Info("由于上下文取消,后处理循环已停止")
				return
			case <-ticker.C:
				if err := p.processNextBatch(); err != nil {
					p.logger.Error("处理批次时出错", logger.Error(err))
				}
			}
		}
	}()
	return nil
}

// Stop 停止后处理循环
func (p *PostProcessor) Stop() error {
	p.logger.Info("正在停止后处理循环")
	p.cancel()
	p.wg.Wait()
	return nil
}

// TranscriptionBatch 表示要处理的一批转写
type TranscriptionBatch struct {
	ID               int64                       `json:"id"`
	Content          string                      `json:"content"`
	ContentProcessed string                      `json:"content_processed"`
	SpeakerType      string                      `json:"speaker_type"`
	Callsign         string                      `json:"callsign"`
	Clearances       []sqlite.ExtractedClearance `json:"clearances"`
	Timestamp        time.Time                   `json:"timestamp"`
}

// processNextBatch 处理下一批未处理的转写
func (p *PostProcessor) processNextBatch() error {
	// 获取未处理的转写
	records, err := p.transcriptionStorage.GetUnprocessedTranscriptions(p.batchSize)
	if err != nil {
		return fmt.Errorf("获取未处理转写失败: %w", err)
	}

	if len(records) == 0 {
		p.logger.Debug("没有找到未处理的转写")
		return nil // 没有要处理的内容
	}

	p.logger.Debug("正在处理一批转写", logger.Int("count", len(records)))

	// 获取第一条记录的频率名称(假设所有记录来自同一频率)
	var frequencyName string
	var frequencyID string
	if len(records) > 0 {
		frequencyID = records[0].FrequencyID
		var err error
		frequencyName, err = p.getFrequencyName(frequencyID)
		if err != nil {
			p.logger.Error("获取频率名称失败", logger.Error(err))
			frequencyName = frequencyID // 使用 ID 作为后备
		}
	}

	// 获取最后 N 条已处理的转写作为上下文
	var contextRecords []*sqlite.TranscriptionRecord
	if frequencyID != "" && p.config.ContextTranscriptions > 0 {
		contextRecords, err = p.transcriptionStorage.GetLastProcessedTranscriptions(frequencyID, p.config.ContextTranscriptions)
		if err != nil {
			p.logger.Error("获取上下文转写失败", logger.Error(err))
			// 不带上下文继续运行
		} else {
			p.logger.Debug("包含上下文转写", logger.Int("count", len(contextRecords)))
		}
	}

	// 准备要处理的转写批次
	var batch []TranscriptionBatch

	// 将上下文和未处理的转写都添加到批次中
	for _, record := range contextRecords {
		batch = append(batch, TranscriptionBatch{
			ID:               record.ID,
			Content:          record.Content,
			ContentProcessed: record.ContentProcessed,
			SpeakerType:      record.SpeakerType,
			Callsign:         record.Callsign,
			Clearances:       []sqlite.ExtractedClearance{}, // 上下文记录为空
			Timestamp:        record.CreatedAt,
		})
	}

	for _, record := range records {
		batch = append(batch, TranscriptionBatch{
			ID:               record.ID,
			Content:          record.Content,
			ContentProcessed: "",
			SpeakerType:      "",
			Callsign:         "",
			Clearances:       []sqlite.ExtractedClearance{}, // 将由 AI 填充
			Timestamp:        record.CreatedAt,
		})
	}

	// 按时间戳对批次排序(最旧到最新)
	p.sortBatchByTimestamp(batch)

	// 将批次转换为 JSON
	batchJSON, err := json.MarshalIndent(batch, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化转写批次失败: %w", err)
	}

	// 使用模板渲染器生成带当前空域数据的系统提示词
	systemPrompt, err := p.templateRenderer.RenderPostProcessorTemplate(p.config.SystemPromptPath)
	if err != nil {
		p.logger.Error("渲染系统提示词模板失败", logger.Error(err))
		// 标记所有记录为失败以防止无限重试
		for _, record := range records {
			if updateErr := p.transcriptionStorage.UpdateProcessedTranscription(
				record.ID,
				"[TEMPLATE_RENDER_FAILED]",
				"UNKNOWN",
				"",
			); updateErr != nil {
				p.logger.Error("将转写标记为失败时出错",
					logger.Int64("id", record.ID),
					logger.Error(updateErr))
			}
		}
		return err
	}

	// 用户输入仅包含频率和转写数据
	userInput := fmt.Sprintf("Radio Frequency:\n%s\n\nTransmissions Log:\n%s",
		frequencyName,
		string(batchJSON))

	// 处理批次
	results, err := p.processBatch(systemPrompt, userInput)
	if err != nil {
		p.logger.Error("处理批次失败", logger.Error(err))
		// 标记所有记录为失败以防止无限重试
		for _, record := range records {
			if updateErr := p.transcriptionStorage.UpdateProcessedTranscription(
				record.ID,
				"[PROCESSING_FAILED]",
				"UNKNOWN",
				"",
			); updateErr != nil {
				p.logger.Error("将转写标记为失败时出错",
					logger.Int64("id", record.ID),
					logger.Error(updateErr))
			}
		}
		return err
	}

	// 检查是否得到任何结果
	if len(results) == 0 {
		p.logger.Warn("OpenAI API 没有返回结果,标记批次为失败")
		// 标记所有记录为失败以防止无限重试
		for _, record := range records {
			if updateErr := p.transcriptionStorage.UpdateProcessedTranscription(
				record.ID,
				"[NO_RESULTS_FROM_API]",
				"UNKNOWN",
				"",
			); updateErr != nil {
				p.logger.Error("将转写标记为失败时出错",
					logger.Int64("id", record.ID),
					logger.Error(updateErr))
			}
		}
		return nil
	}

	// 更新数据库中已处理的转写
	for _, result := range results {
		// 跳过处理内容为空或已处理的转写(上下文)
		if result.ContentProcessed == "" {
			p.logger.Warn("跳过处理内容为空的结果 - 这表示 OpenAI 返回了结果但未填写 content_processed 字段",
				logger.Int64("id", result.ID),
				logger.String("original_content", result.Content),
				logger.String("speaker_type", result.SpeakerType),
				logger.String("callsign", result.Callsign))
			continue
		}

		// 跳过已处理的上下文转写
		isContextRecord := false
		for _, contextRecord := range contextRecords {
			if contextRecord.ID == result.ID {
				isContextRecord = true
				break
			}
		}
		if isContextRecord {
			p.logger.Debug("跳过已处理的上下文记录",
				logger.Int64("id", result.ID))
			continue
		}

		// 更新数据库
		if err := p.transcriptionStorage.UpdateProcessedTranscription(
			result.ID,
			result.ContentProcessed,
			result.SpeakerType,
			result.Callsign,
		); err != nil {
			p.logger.Error("更新已处理转写失败",
				logger.Int64("id", result.ID),
				logger.Error(err))
			continue
		}

		// 如果这是带许可的 ATC 通信,则处理许可
		if result.SpeakerType == "ATC" && len(result.Clearances) > 0 {
			for _, clearance := range result.Clearances {
				clearanceRecord := &sqlite.ClearanceRecord{
					TranscriptionID: result.ID,
					Callsign:        clearance.Callsign,
					ClearanceType:   clearance.Type,
					ClearanceText:   clearance.Text,
					Runway:          clearance.Runway,
					Timestamp:       result.Timestamp,
					Status:          "issued",
					CreatedAt:       time.Now().UTC(),
				}

				clearanceID, err := p.clearanceStorage.StoreClearance(clearanceRecord)
				if err != nil {
					p.logger.Error("存储许可失败",
						logger.String("callsign", clearance.Callsign),
						logger.String("type", clearance.Type),
						logger.Error(err))
					continue
				}

				// 设置用于广播的 ID
				clearanceRecord.ID = clearanceID

				// 通过 WebSocket 广播许可事件
				p.broadcastClearanceEvent(clearanceRecord)

				p.logger.Info("已存储许可",
					logger.String("callsign", clearance.Callsign),
					logger.String("type", clearance.Type),
					logger.String("runway", clearance.Runway),
					logger.Int64("clearance_id", clearanceID))
			}
		}

		// 找到原始记录以广播
		var record *sqlite.TranscriptionRecord
		for _, r := range records {
			if r.ID == result.ID {
				record = r
				break
			}
		}

		if record == nil {
			p.logger.Error("找不到用于广播的原始记录",
				logger.Int64("id", result.ID))
			continue
		}

		// 用已处理内容更新记录
		record.ContentProcessed = result.ContentProcessed
		record.SpeakerType = result.SpeakerType
		record.Callsign = result.Callsign
		record.IsProcessed = true

		// 记录已处理的转写而不是广播
		p.logProcessedTranscription(record)
	}

	return nil
}

// processBatch 处理一批转写
func (p *PostProcessor) processBatch(systemPrompt string, userInput string) ([]TranscriptionBatch, error) {
	// 调用 OpenAI API 处理批次
	results, err := p.openaiClient.PostProcessBatch(p.ctx, systemPrompt, userInput, p.config.Model)
	if err != nil {
		return nil, fmt.Errorf("批量后处理失败: %w", err)
	}

	return results, nil
}

// getFrequencyName 从频率 ID 检索频率名称
func (p *PostProcessor) getFrequencyName(frequencyID string) (string, error) {
	// 检查我们是否在缓存中有该频率名称
	if name, ok := p.frequencyNames[frequencyID]; ok {
		return name, nil
	}

	// 如果不在缓存中,尝试从数据库获取
	// 这需要添加从数据库获取频率信息的方法
	// 目前,我们仅返回 ID 作为名称
	return frequencyID, nil
}

// logProcessedTranscription 将已处理的转写记录到服务器控制台并广播给 WebSocket 客户端
func (p *PostProcessor) logProcessedTranscription(record *sqlite.TranscriptionRecord) {
	// 在 debug 级别记录已处理的转写
	p.logger.Debug("已处理转写",
		logger.Int64("id", record.ID),
		logger.String("frequency_id", record.FrequencyID),
		logger.String("original_content", record.Content),
		logger.String("processed_content", record.ContentProcessed),
		logger.String("speaker_type", record.SpeakerType),
		logger.String("callsign", record.Callsign),
		logger.Time("timestamp", record.CreatedAt))

	// 如果启用,写入文件日志记录器
	if p.fileLogger != nil {
		if err := p.fileLogger.LogProcessed(record.FrequencyID, record.CreatedAt, record.SpeakerType, record.Callsign, record.ContentProcessed); err != nil {
			p.logger.Error("将已处理转写写入日志文件失败", logger.Error(err))
		}
	}

	// 创建用于更新原始消息的 WebSocket 消息
	message := &websocket.Message{
		Type: "transcription_update",
		Data: map[string]interface{}{
			"id":                record.ID,
			"frequency_id":      record.FrequencyID,
			"text":              record.Content,
			"timestamp":         record.CreatedAt,
			"is_complete":       true,
			"is_processed":      true,
			"content_processed": record.ContentProcessed,
			"speaker_type":      record.SpeakerType,
			"callsign":          record.Callsign,
		},
	}

	// 记录我们即将发送的消息
	p.logger.Debug("正在向 WebSocket 客户端广播已处理的转写",
		logger.Int64("id", record.ID),
		logger.String("frequency_id", record.FrequencyID))

	// 广播给 WebSocket 客户端
	p.wsServer.Broadcast(message)
}

// sortBatchByTimestamp 按时间戳对一批转写排序(最旧到最新)
func (p *PostProcessor) sortBatchByTimestamp(batch []TranscriptionBatch) {
	// 按时间戳对批次排序(升序 - 最旧的在前)
	sort.Slice(batch, func(i, j int) bool {
		return batch[i].Timestamp.Before(batch[j].Timestamp)
	})
}

// broadcastClearanceEvent 通过 WebSocket 广播许可事件
func (p *PostProcessor) broadcastClearanceEvent(clearance *sqlite.ClearanceRecord) {
	message := &websocket.Message{
		Type: "clearance_issued",
		Data: map[string]interface{}{
			"id":             clearance.ID,
			"callsign":       clearance.Callsign,
			"clearance_type": clearance.ClearanceType,
			"clearance_text": clearance.ClearanceText,
			"runway":         clearance.Runway,
			"timestamp":      clearance.Timestamp,
			"status":         clearance.Status,
		},
	}

	// 记录我们即将发送的消息
	p.logger.Debug("正在向 WebSocket 客户端广播许可事件",
		logger.Int64("id", clearance.ID),
		logger.String("callsign", clearance.Callsign),
		logger.String("type", clearance.ClearanceType))

	// 广播给 WebSocket 客户端
	p.wsServer.Broadcast(message)
}
