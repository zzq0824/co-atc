package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/atcchat"
	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/frequencies"
	"github.com/yegors/co-atc/internal/reference"
	"github.com/yegors/co-atc/internal/simulation"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/weather"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

var defaultCORSAllowedOrigins = []string{"*"}

// Router 是 API 路由器
type Router struct {
	handler    *Handler
	middleware *Middleware
	config     *config.Config
	logger     *logger.Logger
}

// NewRouter 创建一个新的 API 路由器
func NewRouter(adsbService *adsb.Service, frequenciesService *frequencies.Service, weatherService *weather.Service, atcChatService *atcchat.Service, simulationService *simulation.Service, refService *reference.Service, config *config.Config, logger *logger.Logger, wsServer *websocket.Server, transcriptionStorage *sqlite.TranscriptionStorage, clearanceStorage *sqlite.ClearanceStorage) *Router {
	return &Router{
		handler:    NewHandler(adsbService, frequenciesService, weatherService, atcChatService, simulationService, refService, config, logger, wsServer, transcriptionStorage, clearanceStorage),
		middleware: NewMiddleware(logger),
		config:     config,
		logger:     logger.Named("api-router"),
	}
}

// Routes 返回 API 路由
func (r *Router) Routes() http.Handler {
	router := chi.NewRouter()

	// 中间件
	router.Use(r.middleware.RequestID)
	router.Use(r.middleware.Logger)
	router.Use(r.middleware.Recoverer)
	router.Use(r.middleware.CORS(defaultCORSAllowedOrigins))

	// API 路由
	router.Route("/api/v1", func(router chi.Router) {
		// 飞行器路由
		router.Get("/aircraft", r.handler.GetAllAircraft)
		router.Get("/aircraft/{id}", r.handler.GetAircraftByHex)
		router.Get("/aircraft/{id}/tracks", r.handler.GetAircraftTracks)

		// 频率路由
		router.Get("/frequencies", r.handler.GetAllFrequencies)
		router.Get("/frequencies/{id}", r.handler.GetFrequencyByID)

		// 音频流路由
		router.Get("/stream/{id}", r.handler.StreamAudio)
		router.Head("/stream/{id}", r.handler.StreamAudio) // 添加对 HEAD 请求的支持

		// WebSocket 路由
		router.Get("/ws", r.handler.HandleWebSocket)

		// 转写路由
		router.Get("/transcriptions", r.handler.GetAllTranscriptions)
		router.Get("/transcriptions/frequency/{id}", r.handler.GetTranscriptionsByFrequency)
		router.Get("/transcriptions/time-range", r.handler.GetTranscriptionsByTimeRange)
		router.Get("/transcriptions/speaker/{type}", r.handler.GetTranscriptionsBySpeaker)
		router.Get("/transcriptions/callsign/{callsign}", r.handler.GetTranscriptionsByCallsign)

		// 健康检查
		router.Get("/health", r.handler.GetHealth)

		// 配置
		router.Get("/config", r.handler.GetConfig)
		router.Get("/adsb/source", r.handler.GetADSBSourceStatus)

		// 站点配置
		router.Get("/station", r.handler.GetStationConfig)    // 站点配置的新路由
		router.Post("/station", r.handler.SetStationOverride) // 站点覆盖的新路由

		// 天气数据
		router.Get("/wx", r.handler.GetWeatherData)

		// 参考数据(机场、导航台、跑道)
		router.Get("/airports", r.handler.GetAirports)
		router.Get("/airports/{ident}", r.handler.GetAirportByIdent)
		router.Get("/heliports", r.handler.GetHeliports)
		router.Get("/navaids", r.handler.GetNavaids)
		router.Get("/navaids/{ident}", r.handler.GetNavaidByIdent)
		router.Get("/runways", r.handler.GetRunways)

		// ATC 聊天路由
		router.Post("/atc-chat/session", r.handler.CreateATCChatSession)
		router.Delete("/atc-chat/session/{sessionId}", r.handler.EndATCChatSession)
		router.Get("/atc-chat/session/{sessionId}/status", r.handler.GetATCChatSessionStatus)
		router.Post("/atc-chat/session/{sessionId}/update-context", r.handler.UpdateATCChatSessionContext)
		router.Get("/atc-chat/sessions", r.handler.GetATCChatSessions)
		router.Get("/atc-chat/airspace-status", r.handler.GetATCChatAirspaceStatus)
		router.Get("/atc-chat/ws/{sessionId}", r.handler.HandleATCChatWebSocket)

		// 模拟路由
		router.Post("/simulation/aircraft", r.handler.CreateSimulatedAircraft)
		router.Put("/simulation/aircraft/{hex}/controls", r.handler.UpdateSimulationControls)
		router.Delete("/simulation/aircraft/{hex}", r.handler.RemoveSimulatedAircraft)
		router.Get("/simulation/aircraft", r.handler.GetSimulatedAircraft)
	})

	// 从硬编码的 web 目录提供静态文件
	staticHandler := NewStaticFileHandler("www", r.logger)
	router.Handle("/*", staticHandler)

	return router
}
