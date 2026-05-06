package adsb

import (
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// WebSocketHandler 处理 ADSB 数据的传入 WebSocket 消息
type WebSocketHandler struct {
	service *Service
	logger  *logger.Logger
}

// NewWebSocketHandler 创建一个新的 WebSocket 消息处理器
func NewWebSocketHandler(service *Service, logger *logger.Logger) *WebSocketHandler {
	return &WebSocketHandler{
		service: service,
		logger:  logger.Named("adsb-ws-handler"),
	}
}

// HandleMessage 处理传入的 WebSocket 消息
func (h *WebSocketHandler) HandleMessage(client *websocket.Client, messageType string, data map[string]interface{}) error {
	switch messageType {
	case websocket.MessageTypeAircraftBulkRequest:
		return h.handleBulkRequest(client, data)
	case websocket.MessageTypeFilterUpdate:
		return h.handleFilterUpdate(client, data)
	case websocket.MessageTypeSimulationControlUpdate:
		return h.handleSimulationControlUpdate(client, data)
	default:
		h.logger.Debug("未处理的消息类型", logger.String("type", messageType))
		return nil
	}
}

// handleBulkRequest 处理批量飞行器数据请求
func (h *WebSocketHandler) handleBulkRequest(client *websocket.Client, data map[string]interface{}) error {
	h.logger.Debug("正在处理批量飞行器数据请求")

	// 从请求中解析过滤器
	filters := make(map[string]interface{})
	if filtersData, ok := data["filters"].(map[string]interface{}); ok {
		filters = filtersData
	}

	// 从服务获取批量飞行器数据
	response, err := h.service.HandleBulkRequest(filters)
	if err != nil {
		h.logger.Error("获取批量飞行器数据失败", logger.Error(err))
		return err
	}

	// 将响应发回客户端
	message := &websocket.Message{
		Type: websocket.MessageTypeAircraftBulkResponse,
		Data: map[string]interface{}{
			"aircraft": response.Aircraft,
			"count":    response.Count,
			"counts":   response.Counts,
		},
	}

	// 发送给特定客户端(非广播)
	return h.sendToClient(client, message)
}

// handleFilterUpdate 处理来自客户端的过滤器更新消息
// 注意:服务端过滤已被移除。所有过滤都在客户端进行。
// 该处理器为向后兼容保留,但不执行任何操作。
func (h *WebSocketHandler) handleFilterUpdate(client *websocket.Client, data map[string]interface{}) error {
	h.logger.Debug("收到过滤器更新(过滤仅在客户端进行)")
	return nil
}

// handleSimulationControlUpdate 处理模拟控制更新消息
func (h *WebSocketHandler) handleSimulationControlUpdate(client *websocket.Client, data map[string]interface{}) error {
	h.logger.Debug("正在处理模拟控制更新")

	// 解析必填字段
	hex, ok := data["hex"].(string)
	if !ok || hex == "" {
		h.logger.Warn("模拟控制更新中 hex 缺失或无效")
		return nil
	}

	heading, _ := data["heading"].(float64)
	speed, _ := data["speed"].(float64)
	verticalRate, _ := data["vertical_rate"].(float64)

	// 通过服务更新模拟控制
	if err := h.service.UpdateSimulationControls(hex, heading, speed, verticalRate); err != nil {
		h.logger.Error("更新模拟控制失败",
			logger.String("hex", hex),
			logger.Error(err))
		return err
	}

	h.logger.Info("已通过 WebSocket 更新模拟控制",
		logger.String("hex", hex),
		logger.Float64("heading", heading),
		logger.Float64("speed", speed),
		logger.Float64("vertical_rate", verticalRate))

	return nil
}

// sendToClient 向特定客户端发送消息
func (h *WebSocketHandler) sendToClient(client *websocket.Client, message *websocket.Message) error {
	//messageData, err := json.Marshal(message)
	//if err != nil {
	//	return err
	//}

	//h.logger.Debug("Sending message to client",
	//	logger.String("type", message.Type),
	//	logger.Int("data_size", len(messageData)))

	// 向特定客户端发送消息
	if client.SendMessage(message) {
		return nil
	} else {
		h.logger.Warn("客户端发送通道已满,正在丢弃消息")
		return nil
	}
}
