package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yegors/co-atc/internal/adsb"
	"github.com/yegors/co-atc/internal/api"
	"github.com/yegors/co-atc/internal/atcchat"
	"github.com/yegors/co-atc/internal/config"
	"github.com/yegors/co-atc/internal/frequencies"
	"github.com/yegors/co-atc/internal/reference"
	"github.com/yegors/co-atc/internal/simulation"
	"github.com/yegors/co-atc/internal/storage/sqlite"
	"github.com/yegors/co-atc/internal/templating"
	"github.com/yegors/co-atc/internal/weather"
	"github.com/yegors/co-atc/internal/websocket"
	"github.com/yegors/co-atc/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const defaultDBRetentionDays = 7

// refAdapter 包装 reference.Service 以实现 adsb.ReferenceService 接口
type refAdapter struct {
	service *reference.Service
}

func (a *refAdapter) LookupAircraft(hex string) *adsb.ReferenceAircraftInfo {
	info := a.service.LookupAircraft(hex)
	if info == nil {
		return nil
	}
	return &adsb.ReferenceAircraftInfo{
		Hex:               info.Hex,
		Registration:      info.Registration,
		TypeCode:          info.TypeCode,
		ManufacturerModel: info.ManufacturerModel,
		Year:              info.Year,
		Owner:             info.Owner,
	}
}

func (a *refAdapter) LookupAirline(code string) string {
	return a.service.LookupAirline(code)
}

func (a *refAdapter) LookupAirlineCountry(code string) string {
	return a.service.LookupAirlineCountry(code)
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "", "配置文件路径(可选,默认在 configs/ 与根目录中搜索)")
	flag.Parse()

	// 使用回退逻辑加载配置
	cfg, resolvedConfigPath, err := config.LoadWithFallbackAndPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置错误:%v\n", err)
		os.Exit(1)
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "无效的配置:%v\n", err)
		os.Exit(1)
	}

	// 创建日志记录器
	log, err := logger.New(logger.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建日志记录器错误:%v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	log.Info("启动 Co-ATC 服务器",
		logger.String("version", "0.1.0"),
		logger.String("config_path", resolvedConfigPath),
	)

	// 创建 ADS-B 组件
	adsbClient := adsb.NewClient(
		cfg.ADSB,
		cfg.Station.Latitude,
		cfg.Station.Longitude,
		time.Duration(cfg.Server.ReadTimeoutSecs)*time.Second,
		log,
	)

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := adsbClient.ValidateSource(probeCtx); err != nil {
		probeCancel()
		fatalLog := log.WithOptions(zap.AddStacktrace(zapcore.PanicLevel))
		fatalLog.Fatal("ADS-B 数据源验证失败", logger.Error(err), logger.String("source_type", cfg.ADSB.SourceType))
	}
	probeCancel()
	log.Info("ADS-B 数据源验证成功", logger.String("source_type", cfg.ADSB.SourceType))
	// 处理器已移至服务中

	// 创建 SQLite 存储
	var adsbStorage adsb.Storage

	// 生成今天的数据库文件名
	today := time.Now().Format("2006-01-02")
	dbFilename := fmt.Sprintf("co-atc-%s.db", today)
	dbPath := filepath.Join(cfg.Storage.SQLiteBasePath, dbFilename)

	// 确保目录存在
	dbDir := cfg.Storage.SQLiteBasePath
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Error("创建数据库目录失败", logger.Error(err), logger.String("path", dbDir))
		os.Exit(1)
	}

	if err := cleanupOldDailyDatabases(dbDir, dbPath, cfg.Storage.DBRetentionDays, time.Now().UTC(), log); err != nil {
		log.Warn("清理旧数据库文件失败",
			logger.Error(err),
			logger.String("path", dbDir),
			logger.Int("retention_days", cfg.Storage.DBRetentionDays))
	}

	log.Info("使用每日数据库", logger.String("path", dbPath))

	// 创建无保留设置的 SQLite 存储
	sqliteStorage, err := sqlite.NewAircraftStorage(
		dbPath,
		log,
	)
	if err != nil {
		log.Error("创建 SQLite 存储失败", logger.Error(err))
		os.Exit(1)
	}
	defer sqliteStorage.Close()
	adsbStorage = sqliteStorage
	log.Info("使用 SQLite 存储", logger.String("path", dbPath))

	// 创建转写存储
	transcriptionStorage := sqlite.NewTranscriptionStorage(sqliteStorage.GetDB(), log)

	// 创建放行许可存储
	clearanceStorage := sqlite.NewClearanceStorage(sqliteStorage.GetDB(), log)

	// 创建 WebSocket 服务器
	wsServer := websocket.NewServer(log)

	// 启动 WebSocket 服务器
	go wsServer.Run()

	// 创建仿真服务
	simulationService := simulation.NewService(log)

	adsbService := adsb.NewService(
		adsbClient,
		adsbStorage,
		time.Duration(cfg.ADSB.FetchIntervalSecs)*time.Second,
		log,
		cfg.Station,
		cfg.ADSB,
		cfg.FlightPhases,
		wsServer,
		simulationService,
	)

	// 加载参考数据(飞行器、航空公司、机场、跑道、导航台)
	refService, err := reference.NewService(reference.ServiceConfig{
		AircraftCSVPath:    cfg.Reference.AircraftCSVPath,
		AirlinesDATPath:    cfg.Reference.AirlinesDATPath,
		AirportsCSVPath:    cfg.Reference.AirportsCSVPath,
		FrequenciesCSVPath: cfg.Reference.FrequenciesCSVPath,
		RunwaysCSVPath:     cfg.Reference.RunwaysCSVPath,
		NavaidsCSVPath:     cfg.Reference.NavaidsCSVPath,
		StationLat:         cfg.Station.Latitude,
		StationLon:         cfg.Station.Longitude,
		HomeAirportCode:    cfg.Station.AirportCode,
		DisplayRangeNM:     cfg.Station.DisplayRangeNM,
		ExtensionLengthNM:  cfg.Station.RunwayExtensionLengthNM,
	}, log)
	if err != nil {
		log.Warn("加载参考数据失败", logger.Error(err))
	} else {
		adsbService.SetReferenceService(&refAdapter{service: refService})
		adsbService.SetRunwayData(refService.GetHomeRunwayData())
		log.Info("参考数据已加载",
			logger.Int("aircraft_count", refService.AircraftCount()),
			logger.Int("airline_count", refService.AirlineCount()))
	}

	// 为 ADSB 创建并设置 WebSocket 消息处理器
	wsHandler := adsb.NewWebSocketHandler(adsbService, log)
	wsServer.SetMessageHandler(wsHandler)

	// 启动 ADS-B 服务
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runDatabaseRetentionCleanup(ctx, dbDir, dbPath, cfg.Storage.DBRetentionDays, log)

	if err := adsbService.Start(ctx); err != nil {
		log.Error("启动 ADS-B 服务失败", logger.Error(err))
		os.Exit(1)
	}

	// 首先创建天气服务(模板引擎需要)
	weatherConfigConverted := weather.ConfigWeatherConfig{
		RefreshIntervalMinutes: cfg.Weather.RefreshIntervalMinutes,
		APIBaseURL:             cfg.Weather.APIBaseURL,
		RequestTimeoutSeconds:  cfg.Weather.RequestTimeoutSeconds,
		MaxRetries:             cfg.Weather.MaxRetries,
		FetchMETAR:             cfg.Weather.FetchMETAR,
		FetchTAF:               cfg.Weather.FetchTAF,
		FetchNOTAMs:            cfg.Weather.FetchNOTAMs,
		CacheExpiryMinutes:     cfg.Weather.CacheExpiryMinutes,
	}
	weatherService := weather.NewService(weatherConfigConverted, cfg.Station.AirportCode, log)

	// 启动天气服务
	if err := weatherService.Start(); err != nil {
		log.Error("启动天气服务失败", logger.Error(err))
		os.Exit(1)
	}

	// 创建模板服务
	templateService := templating.NewService(
		adsbService,
		weatherService,
		transcriptionStorage,
		nil, // 频率服务尚未可用
		cfg,
		log,
	)

	// 创建频率服务
	frequenciesService := frequencies.NewService(cfg, log, wsServer, transcriptionStorage, sqliteStorage, clearanceStorage, templateService)

	// 用频率服务更新模板服务
	templateService = templating.NewService(
		adsbService,
		weatherService,
		transcriptionStorage,
		frequenciesService,
		cfg,
		log,
	)

	// 启动频率服务
	if err := frequenciesService.Start(ctx); err != nil {
		log.Error("启动频率服务失败", logger.Error(err))
		os.Exit(1)
	}

	// 创建 ATC 聊天服务(如已启用)
	var atcChatService *atcchat.Service
	if cfg.ATCChat.Enabled {
		log.Info("创建 ATC 聊天服务")
		atcChatService, err = atcchat.NewService(
			templateService,
			cfg,
			log,
		)
		if err != nil {
			log.Error("创建 ATC 聊天服务失败", logger.Error(err))
			// 不使用 ATC 聊天服务继续运行,而不是失败
			atcChatService = nil
		} else {
			log.Info("ATC 聊天服务创建成功")
		}
	} else {
		log.Info("ATC 聊天服务在配置中已禁用")
	}

	// 创建 API 路由器
	router := api.NewRouter(adsbService, frequenciesService, weatherService, atcChatService, simulationService, refService, cfg, log, wsServer, transcriptionStorage, clearanceStorage)

	// --- 多 HTTP 服务器设置 ---
	var servers []*http.Server
	allPorts := []int{cfg.Server.Port}       // 从主端口开始
	if len(cfg.Server.AdditionalPorts) > 0 { // 仅当存在额外端口时才追加
		allPorts = append(allPorts, cfg.Server.AdditionalPorts...)
	}

	log.Info("已配置的监听端口", logger.Any("ports", allPorts))

	// 为每个配置的端口启动一个服务器
	for _, port := range allPorts {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, port)
		server := &http.Server{
			Addr:         addr,
			Handler:      router.Routes(), // 所有服务器使用相同的主路由器
			ReadTimeout:  time.Duration(cfg.Server.ReadTimeoutSecs) * time.Second,
			WriteTimeout: time.Duration(cfg.Server.WriteTimeoutSecs) * time.Second,
			IdleTimeout:  time.Duration(cfg.Server.IdleTimeoutSecs) * time.Second,
		}
		servers = append(servers, server)

		go func(s *http.Server) {
			log.Info("启动 HTTP 服务器", logger.String("addr", s.Addr))
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("HTTP 服务器启动错误", logger.String("addr", s.Addr), logger.Error(err))
				// 如果一个服务器启动失败,记录错误。根据需求,
				// 您可能需要在此处 os.Exit(1) 或实现更复杂的错误处理。
			}
		}(server)
	}

	// 等待中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("正在关闭服务器...")

	// 首先停止后台服务
	log.Info("正在停止天气服务...")
	weatherService.Stop()
	log.Info("天气服务已停止。")

	log.Info("正在停止频率服务...")
	frequenciesService.Stop()
	log.Info("频率服务已停止。")

	// 如果已创建则停止 ATC 聊天服务
	if atcChatService != nil {
		log.Info("正在停止 ATC 聊天服务...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := atcChatService.Shutdown(shutdownCtx); err != nil {
			log.Error("关闭 ATC 聊天服务时出错", logger.Error(err))
		} else {
			log.Info("ATC 聊天服务已停止。")
		}
		shutdownCancel()
	}

	// 停止所有活动的转写处理器
	// 当我们集成转写服务时,频率服务将处理此项

	log.Info("正在停止 ADS-B 服务...")
	adsbService.Stop()
	log.Info("ADS-B 服务已停止。")

	// 取消主上下文
	cancel()

	// 关闭所有 HTTP 服务器
	log.Info("正在关闭 HTTP 服务器...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second) // 略微增加多服务器的超时时间
	defer shutdownCancel()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func(srv *http.Server) {
			defer wg.Done()
			log.Info("尝试关闭 HTTP 服务器", logger.String("addr", srv.Addr))
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Error("HTTP 服务器关闭错误", logger.String("addr", srv.Addr), logger.Error(err))
			} else {
				log.Info("HTTP 服务器关闭完成", logger.String("addr", srv.Addr))
			}
		}(s)
	}
	wg.Wait() // 等待所有服务器关闭完成

	log.Info("所有 HTTP 服务器已关闭。")

	log.Info("服务器已完全停止")
}

func runDatabaseRetentionCleanup(ctx context.Context, dbDir, activeDBPath string, keepDays int, log *logger.Logger) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ensureTodayDatabaseFile(dbDir, log); err != nil {
				log.Warn("创建今日数据库文件失败",
					logger.Error(err),
					logger.String("path", dbDir))
			}

			if err := cleanupOldDailyDatabases(dbDir, activeDBPath, keepDays, time.Now().UTC(), log); err != nil {
				log.Warn("周期性数据库保留清理失败",
					logger.Error(err),
					logger.String("path", dbDir),
					logger.Int("retention_days", keepDays))
			}
		}
	}
}

func ensureTodayDatabaseFile(dbDir string, log *logger.Logger) error {
	today := time.Now().Format("2006-01-02")
	todayDBPath := filepath.Join(dbDir, fmt.Sprintf("co-atc-%s.db", today))

	if _, err := os.Stat(todayDBPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("获取今日数据库状态:%w", err)
	}

	storage, err := sqlite.NewAircraftStorage(todayDBPath, log)
	if err != nil {
		return fmt.Errorf("初始化今日数据库:%w", err)
	}
	defer storage.Close()

	log.Info("已创建新的每日数据库文件",
		logger.String("path", todayDBPath))

	return nil
}

func cleanupOldDailyDatabases(dbDir, activeDBPath string, keepDays int, now time.Time, log *logger.Logger) error {
	if keepDays <= 0 {
		keepDays = defaultDBRetentionDays
	}

	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return fmt.Errorf("读取数据库目录:%w", err)
	}

	absActiveDBPath, _ := filepath.Abs(activeDBPath)
	cutoffDate := now.UTC().Truncate(24 * time.Hour).AddDate(0, 0, -(keepDays - 1))

	deletedCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		if !strings.HasPrefix(fileName, "co-atc-") || !strings.HasSuffix(fileName, ".db") {
			continue
		}

		dateStr := strings.TrimSuffix(strings.TrimPrefix(fileName, "co-atc-"), ".db")
		fileDate, parseErr := time.Parse("2006-01-02", dateStr)
		if parseErr != nil {
			continue
		}

		if !fileDate.Before(cutoffDate) {
			continue
		}

		filePath := filepath.Join(dbDir, fileName)
		absFilePath, _ := filepath.Abs(filePath)
		if absFilePath == absActiveDBPath {
			continue
		}

		if removeErr := os.Remove(filePath); removeErr != nil {
			return fmt.Errorf("删除旧数据库 '%s':%w", filePath, removeErr)
		}
		deletedCount++
		log.Info("已删除旧数据库文件",
			logger.String("path", filePath),
			logger.String("file_date", fileDate.Format("2006-01-02")))
	}

	if deletedCount > 0 {
		log.Info("数据库保留清理完成",
			logger.Int("deleted_files", deletedCount),
			logger.Int("retention_days", keepDays))
	}

	return nil
}
