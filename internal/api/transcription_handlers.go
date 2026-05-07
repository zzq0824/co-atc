package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/yegors/co-atc/pkg/logger"
)

// HandleWebSocket 处理 WebSocket 连接
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	h.logger.Info("收到 WebSocket 连接请求")

	// 处理 WebSocket 连接
	h.wsServer.HandleConnection(w, r)
}

// GetAllTranscriptions 返回所有带分页的转写
func (h *Handler) GetAllTranscriptions(w http.ResponseWriter, r *http.Request) {
	// 解析分页参数
	limit, offset := parsePaginationParams(r)

	// 从存储获取转写
	transcriptions, err := h.transcriptionStorage.GetTranscriptions(limit, offset)
	if err != nil {
		h.logger.Error("检索转写失败", logger.Error(err))
		http.Error(w, "Failed to retrieve transcriptions", http.StatusInternalServerError)
		return
	}

	// 创建响应
	response := map[string]interface{}{
		"timestamp":      time.Now(),
		"count":          len(transcriptions),
		"transcriptions": transcriptions,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// GetTranscriptionsByFrequency 返回特定频率的转写
func (h *Handler) GetTranscriptionsByFrequency(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取频率 ID
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Missing frequency ID", http.StatusBadRequest)
		return
	}

	// 解析分页参数
	limit, offset := parsePaginationParams(r)

	// 从存储获取转写
	transcriptions, err := h.transcriptionStorage.GetTranscriptionsByFrequency(id, limit, offset)
	if err != nil {
		h.logger.Error("按频率检索转写失败", logger.Error(err))
		http.Error(w, "Failed to retrieve transcriptions", http.StatusInternalServerError)
		return
	}

	// 创建响应
	response := map[string]interface{}{
		"timestamp":      time.Now(),
		"frequency_id":   id,
		"count":          len(transcriptions),
		"transcriptions": transcriptions,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// GetTranscriptionsByTimeRange 返回时间范围内的转写
func (h *Handler) GetTranscriptionsByTimeRange(w http.ResponseWriter, r *http.Request) {
	// 解析时间范围参数
	startTime, endTime, err := parseTimeRangeParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 解析分页参数
	limit, offset := parsePaginationParams(r)

	// 从存储获取转写
	transcriptions, err := h.transcriptionStorage.GetTranscriptionsByTimeRange(startTime, endTime, limit, offset)
	if err != nil {
		h.logger.Error("按时间范围检索转写失败", logger.Error(err))
		http.Error(w, "Failed to retrieve transcriptions", http.StatusInternalServerError)
		return
	}

	// 创建响应
	response := map[string]interface{}{
		"timestamp":      time.Now(),
		"start_time":     startTime,
		"end_time":       endTime,
		"count":          len(transcriptions),
		"transcriptions": transcriptions,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// GetTranscriptionsBySpeaker 按说话人类型返回转写
func (h *Handler) GetTranscriptionsBySpeaker(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取说话人类型
	speakerType := chi.URLParam(r, "type")
	if speakerType == "" {
		http.Error(w, "Missing speaker type", http.StatusBadRequest)
		return
	}

	// 验证说话人类型
	if speakerType != "ATC" && speakerType != "PILOT" {
		http.Error(w, "Invalid speaker type (must be 'ATC' or 'PILOT')", http.StatusBadRequest)
		return
	}

	// 解析分页参数
	limit, offset := parsePaginationParams(r)

	// 从存储获取转写
	transcriptions, err := h.transcriptionStorage.GetTranscriptionsBySpeaker(speakerType, limit, offset)
	if err != nil {
		h.logger.Error("按说话人检索转写失败", logger.Error(err))
		http.Error(w, "Failed to retrieve transcriptions", http.StatusInternalServerError)
		return
	}

	// 创建响应
	response := map[string]interface{}{
		"timestamp":      time.Now(),
		"speaker_type":   speakerType,
		"count":          len(transcriptions),
		"transcriptions": transcriptions,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// GetTranscriptionsByCallsign 按飞行器呼号返回转写
func (h *Handler) GetTranscriptionsByCallsign(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取呼号
	callsign := chi.URLParam(r, "callsign")
	if callsign == "" {
		http.Error(w, "Missing callsign", http.StatusBadRequest)
		return
	}

	// 解析分页参数
	limit, offset := parsePaginationParams(r)

	// 从存储获取转写
	transcriptions, err := h.transcriptionStorage.GetTranscriptionsByCallsign(callsign, limit, offset)
	if err != nil {
		h.logger.Error("按呼号检索转写失败", logger.Error(err))
		http.Error(w, "Failed to retrieve transcriptions", http.StatusInternalServerError)
		return
	}

	// 创建响应
	response := map[string]interface{}{
		"timestamp":      time.Now(),
		"callsign":       callsign,
		"count":          len(transcriptions),
		"transcriptions": transcriptions,
	}

	// 写入响应
	WriteJSON(w, http.StatusOK, response)
}

// 辅助函数
func parsePaginationParams(r *http.Request) (int, int) {
	limit := 100 // 默认限制
	offset := 0  // 默认偏移量

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	return limit, offset
}

func parseTimeRangeParams(r *http.Request) (time.Time, time.Time, error) {
	startTimeStr := r.URL.Query().Get("start_time")
	endTimeStr := r.URL.Query().Get("end_time")

	if startTimeStr == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("缺少 start_time 参数")
	}

	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("无效的 start_time 格式(请使用 RFC3339)")
	}

	endTime := time.Now()
	if endTimeStr != "" {
		endTime, err = time.Parse(time.RFC3339, endTimeStr)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("无效的 end_time 格式(请使用 RFC3339)")
		}
	}

	return startTime, endTime, nil
}
