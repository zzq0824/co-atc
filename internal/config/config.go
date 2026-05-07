package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config 表示主应用程序配置结构体
// 包含所有配置区段
type Config struct {
	Server         ServerConfig         `toml:"server"`          // HTTP 服务器设置
	ADSB           ADSBConfig           `toml:"adsb"`            // 飞行器追踪数据源设置
	Frequencies    FrequenciesConfig    `toml:"frequencies"`     // 无线电频率监控设置
	Logging        LoggingConfig        `toml:"logging"`         // 应用程序日志设置
	Storage        StorageConfig        `toml:"storage"`         // 数据持久化设置
	Station        StationConfig        `toml:"station"`         // 物理位置设置
	Reference      ReferenceConfig      `toml:"reference"`       // 参考数据(飞行器、航司、机场、跑道、导航台)
	Transcription  TranscriptionConfig  `toml:"transcription"`   // 音频转写设置
	PostProcessing PostProcessingConfig `toml:"post_processing"` // 转写的后处理设置
	FlightPhases   FlightPhasesConfig   `toml:"flight_phases"`   // 飞行阶段检测设置
	Weather        WeatherConfig        `toml:"wx"`              // 气象数据获取与缓存设置
	ATCChat        ATCChatConfig        `toml:"atc_chat"`        // ATC Chat 语音助手设置
}

// ServerConfig 包含 HTTP 服务器配置设置
type ServerConfig struct {
	Port             int    `toml:"port"`                  // 服务器主 HTTP 端口
	Host             string `toml:"host"`                  // 绑定的主机地址(例如 127.0.0.1 仅本机,0.0.0.0 表示所有接口)
	ReadTimeoutSecs  int    `toml:"read_timeout_seconds"`  // 读取整个请求的最大时长(0 = 无超时)
	WriteTimeoutSecs int    `toml:"write_timeout_seconds"` // 写入响应的最大时长(0 = 无超时,推荐用于流式传输)
	IdleTimeoutSecs  int    `toml:"idle_timeout_seconds"`  // 启用 keep-alive 时等待下一个请求的最大时长
	AdditionalPorts  []int  `toml:"additional_ports"`      // 额外监听的 HTTP 端口(适用于多接口场景)
}

// ADSBConfig 包含 ADS-B 飞行器追踪数据源配置
type ADSBConfig struct {
	// 数据源选择
	SourceType string `toml:"source_type"` // 数据源类型:"external-rapidapi"、"external-opensky"、"tar1090"、"readsb-api" 或 "readsb-file"

	// 外部 API 数据源设置(当 source_type = "external-rapidapi" 时使用)
	ExternalSourceURL string `toml:"external_source_url"` // 外部 API 的 URL 模板,带有 lat、lon 和 distance 占位符
	APIHost           string `toml:"api_host"`            // API host 头部值(例如 RapidAPI)
	APIKey            string `toml:"api_key"`             // 用于外部服务认证的 API 密钥
	SearchRadiusNM    int    `toml:"search_radius_nm"`    // 外部 API 查询的搜索半径(海里)

	// OpenSky 数据源设置(当 source_type = "external-opensky" 时使用)
	OpenSkyBaseURL               string `toml:"opensky_base_url"`                // OpenSky API 基础 URL(例如 https://opensky-network.org/api)
	OpenSkyTokenURL              string `toml:"opensky_token_url"`               // OAuth2 令牌端点 URL
	OpenSkyAuthMode              string `toml:"opensky_auth_mode"`               // 认证模式:"anonymous" 或 "oauth2"
	OpenSkyOAuth2CredentialsPath string `toml:"opensky_oauth2_credentials_path"` // OAuth2 客户端凭据 JSON 文件路径

	// Tar1090 数据源设置(当 source_type = "tar1090" 时使用)
	Tar1090BaseURL string `toml:"tar1090_base_url"` // 提供 aircraft.json、receiver.json、stats.json 的基础 URL

	// readsb API 设置(当 source_type = "readsb-api" 时使用)
	ReadsbAPIURL string `toml:"readsb_api_url"` // 完整的 readsb API URL(例如 http://host:30152/?all)

	// readsb 文件设置(当 source_type = "readsb-file" 时使用)
	ReadsbDataDir string `toml:"readsb_data_dir"` // readsb 运行时文件的可选覆盖目录(为空时自动检测)

	// 公共设置
	FetchIntervalSecs     int `toml:"fetch_interval_seconds"`      // 获取新飞行器数据的频率(秒)
	SignalLostTimeoutSecs int `toml:"signal_lost_timeout_seconds"` // 飞行器被标记为 signal_lost 的超时时间(秒,默认:60)
}

// LoggingConfig 包含应用程序日志配置
type LoggingConfig struct {
	Level  string `toml:"level"`  // 日志级别:"debug"、"info"、"warn" 或 "error"
	Format string `toml:"format"` // 日志格式:"json"(结构化)或 "console"(可读)
}

// StorageConfig 包含数据持久化配置
type StorageConfig struct {
	Type            string `toml:"type"`              // 存储后端类型(目前仅支持 "sqlite")
	SQLiteBasePath  string `toml:"sqlite_base_path"`  // SQLite 数据库文件的基础路径(实际文件名将生成为 co-atc-YYYY-MM-DD.db)
	DBRetentionDays int    `toml:"db_retention_days"` // 保留每日 SQLite DB 文件的天数(更早的文件会被删除)
}

// StationConfig 包含监控站点的物理位置配置
type StationConfig struct {
	Latitude                float64 `toml:"latitude"`                   // 站点纬度,十进制度数(-90 到 90)
	Longitude               float64 `toml:"longitude"`                  // 站点经度,十进制度数(-180 到 180)
	ElevationFeet           int     `toml:"elevation_feet"`             // 站点海拔(英尺)
	AirportCode             string  `toml:"airport_code"`               // 机场 ICAO 代码(例如 "CYYZ")
	RunwayExtensionLengthNM float64 `toml:"runway_extension_length_nm"` // 跑道延长线长度(海里)
	AirportRangeNM          float64 `toml:"airport_range_nm"`           // 视为该机场范围内飞行器的距离(海里,默认:5.0)
	DisplayRangeNM          float64 `toml:"display_range_nm"`           // 在地图上显示机场、跑道和导航台的范围(海里,默认:100.0)
}

// ReferenceConfig 包含参考数据 CSV 文件的路径
type ReferenceConfig struct {
	AircraftCSVPath    string `toml:"aircraft_csv_path"`            // aircraft.csv 路径(wiedehopf/tar1090-db)
	AirlinesDATPath    string `toml:"airlines_dat_path"`            // airlines.dat 路径(OpenFlights)
	AirportsCSVPath    string `toml:"airports_csv_path"`            // airports.csv 路径(OurAirports)
	FrequenciesCSVPath string `toml:"airport_frequencies_csv_path"` // airport-frequencies.csv 路径(OurAirports)
	RunwaysCSVPath     string `toml:"runways_csv_path"`             // runways.csv 路径(OurAirports)
	NavaidsCSVPath     string `toml:"navaids_csv_path"`             // navaids.csv 路径(OurAirports)
}

// TranscriptionConfig 包含音频转写服务的设置
type TranscriptionConfig struct {
	// OpenAI API 设置
	OpenAIAPIKey string `toml:"openai_api_key"` // 用于转写服务的 OpenAI API 密钥
	Model        string `toml:"model"`          // 使用的 OpenAI 模型(例如 "gpt-4o-transcribe")
	Language     string `toml:"language"`       // 转写主语言(例如 "en" 表示英语)
	PromptPath   string `toml:"prompt_path"`    // 转写系统提示词文件路径

	// 文件日志设置
	LogDir string `toml:"log_dir"` // 转写日志文件的可选目录(按日期和频率追加日志)

	// 音频处理设置
	NoiseReduction string `toml:"noise_reduction"` // 降噪模式:"near_field"、"far_field" 或 "none"
	ChunkMs        int    `toml:"chunk_ms"`        // 音频处理块大小(毫秒)
	BufferSizeKB   int    `toml:"buffer_size_kb"`  // 音频缓冲区大小(KB)

	// FFmpeg 转换设置
	FFmpegPath       string `toml:"ffmpeg_path"`        // FFmpeg 可执行文件路径
	FFmpegSampleRate int    `toml:"ffmpeg_sample_rate"` // 音频采样率(Hz,OpenAI 通常为 24000)
	FFmpegChannels   int    `toml:"ffmpeg_channels"`    // 音频声道数(1 为单声道,2 为立体声)
	FFmpegFormat     string `toml:"ffmpeg_format"`      // 音频格式(例如 "s16le" 表示有符号 16 位小端 PCM)

	// 连接管理
	ReconnectIntervalSec int `toml:"reconnect_interval_sec"` // 失败后重连前等待的秒数
	MaxRetries           int `toml:"max_retries"`            // 最大连接重试次数

	// 语音活动检测(VAD)设置
	TurnDetectionType string  `toml:"turn_detection_type"` // 检测语音轮次的方法(例如 "server_vad")
	PrefixPaddingMs   int     `toml:"prefix_padding_ms"`   // 在检测到的语音之前包含的音频时长(毫秒)
	SilenceDurationMs int     `toml:"silence_duration_ms"` // 视为语音结束的静音时长(毫秒)
	VADThreshold      float64 `toml:"vad_threshold"`       // 语音活动检测阈值(0.0-1.0)

	// API 重试设置
	RetryMaxAttempts      int `toml:"retry_max_attempts"`       // API 调用最大重试次数
	RetryInitialBackoffMs int `toml:"retry_initial_backoff_ms"` // 初始退避时间(毫秒)
	RetryMaxBackoffMs     int `toml:"retry_max_backoff_ms"`     // 最大退避时间(毫秒)

	// HTTP 超时设置
	TimeoutSeconds int `toml:"timeout_seconds"` // OpenAI API 请求的 HTTP 超时(秒)
}

// PostProcessingConfig 包含转写后处理的设置
type PostProcessingConfig struct {
	Enabled               bool   `toml:"enabled"`                // 启用或禁用后处理
	Model                 string `toml:"model"`                  // 用于后处理的 OpenAI 模型
	IntervalSeconds       int    `toml:"interval_seconds"`       // 运行后处理的频率(秒)
	BatchSize             int    `toml:"batch_size"`             // 每批处理的最大转写数量
	ContextTranscriptions int    `toml:"context_transcriptions"` // 用作上下文的已处理转写数量
	SystemPromptPath      string `toml:"system_prompt_path"`     // 系统提示词文件路径
	TimeoutSeconds        int    `toml:"timeout_seconds"`        // OpenAI API 请求的 HTTP 超时(秒)
}

// FrequenciesConfig 包含无线电频率监控的设置
type FrequenciesConfig struct {
	Sources               []FrequencyConfig `toml:"sources"`                 // 要监控的无线电频率列表
	BufferSizeKB          int               `toml:"buffer_size_kb"`          // 音频缓冲区大小(KB)
	ReconnectIntervalSecs int               `toml:"reconnect_interval_secs"` // 流失败后重连前等待的秒数

	// FFmpeg 超时配置
	FFmpegTimeoutSecs        int `toml:"ffmpeg_timeout_secs"`         // FFmpeg 连接超时(秒,0 = 无超时,默认:30)
	FFmpegReconnectDelaySecs int `toml:"ffmpeg_reconnect_delay_secs"` // FFmpeg 重连延迟(秒,默认:2)
}

// FrequencyConfig 包含单个监控无线电频率的配置
type FrequencyConfig struct {
	ID              string  `toml:"id"`               // 该频率的唯一标识符
	Airport         string  `toml:"airport"`          // 机场 ICAO 代码(例如 "CYYZ" 表示多伦多皮尔逊)
	Name            string  `toml:"name"`             // 可读名称(例如 "CYYZ Tower")
	FrequencyMHz    float64 `toml:"frequency_mhz"`    // 实际无线电频率(MHz,例如 118.7)
	URL             string  `toml:"url"`              // 音频流的 URL
	Order           int     `toml:"order"`            // UI 中的显示顺序(数字越小越靠前)
	TranscribeAudio bool    `toml:"transcribe_audio"` // 是否对此频率进行音频转写
}

// FlightPhasesConfig 包含飞行阶段检测的设置
type FlightPhasesConfig struct {
	Enabled                       bool    `toml:"enabled"`                          // 启用增强型飞行阶段检测
	CruiseAltitudeFt              int     `toml:"cruise_altitude_ft"`               // 巡航阶段的最低高度
	DepartureAltitudeFt           int     `toml:"departure_altitude_ft"`            // 离场阶段的最低高度
	TaxiingMinSpeedKts            int     `toml:"taxiing_min_speed_kts"`            // 滑行的最低地速
	TaxiingMaxSpeedKts            int     `toml:"taxiing_max_speed_kts"`            // 滑行的最高地速
	ApproachCenterlineToleranceNM float64 `toml:"approach_centerline_tolerance_nm"` // 离跑道中线的距离
	ApproachMaxDistanceNM         int     `toml:"approach_max_distance_nm"`         // 离跑道入口的最大距离
	ApproachMaxAltitudeFt         int     `toml:"approach_max_altitude_ft"`         // 进近阶段检测的最高高度
	ApproachHeadingToleranceDeg   float64 `toml:"approach_heading_tolerance_deg"`   // 航向对齐容差

	// 超时配置——为清晰起见集中放置
	// 这些参数控制阶段切换的不同时间维度:

	// 1. 长时间不活动飞行器的清理(默认:3600 秒 = 1 小时)
	// 在此时长内未发生任何阶段变化的飞行器会被还原为 NEW
	// 这是停放/不活动飞行器的最终清理机制
	PhaseChangeTimeoutSeconds int `toml:"phase_change_timeout_seconds"`

	// 2. 地面阶段切换防抖(默认:60 秒,但你可能希望设为 1800 = 30 分钟)
	// 防止飞行器短暂停止时发生 TAX→NEW 和 T/D→NEW 的切换
	// 有助于在正常地面运行期间保持阶段稳定
	PhaseTransitionTimeoutSeconds int `toml:"phase_transition_timeout_seconds"`

	// 3. 关键阶段保留(默认:60 秒)
	// 保持 T/O 和 T/D 阶段至少可见此时长
	// 确保飞行员/管制员能看到这些重要事件
	PhasePreservationSeconds int `toml:"phase_preservation_seconds"`

	// 4. 近期起飞检测窗口(默认:30 分钟)
	// 起飞后多长时间内飞行器被视为"刚刚离场"
	// 用于 DEP 阶段资格判定
	RecentTakeoffTimeoutMinutes int `toml:"recent_takeoff_timeout_minutes"`

	// 其他阶段检测参数
	AirportRangeNM float64 `toml:"airport_range_nm"` // 视为"接近机场"的距离

	// 地面检测阈值(新增——使现有常量可配置)
	FlyingMinTASKts         float64 `toml:"flying_min_tas_kts"`        // 视为飞行的最低真空速
	FlyingMinAltFt          float64 `toml:"flying_min_alt_ft"`         // 视为飞行的最低高度
	HelicopterAltMultiplier float64 `toml:"helicopter_alt_multiplier"` // 直升机检测的乘数
	HighSpeedThresholdKts   float64 `toml:"high_speed_threshold_kts"`  // 高于此速度的飞行器始终视为飞行
	HighAltitudeOverrideFt  float64 `toml:"high_altitude_override_ft"` // 高于此高度的飞行器始终视为飞行(应对错误的速度数据)

	// 增强的传感器校验阈值(新增)
	ImpossibleAltDropThresholdFt    float64 `toml:"impossible_alt_drop_threshold_ft"`    // 高于此高度的高度归零始终视为传感器错误
	ImpossibleSpeedDropThresholdKts float64 `toml:"impossible_speed_drop_threshold_kts"` // 高于此速度的速度在高空归零视为传感器错误
	ImpossibleSpeedDropMinAltFt     float64 `toml:"impossible_speed_drop_min_alt_ft"`    // 不可能速度归零检测的最低高度

	// 信号丢失着陆检测
	SignalLostLandingEnabled  bool    `toml:"signal_lost_landing_enabled"`    // 为信号丢失的飞行器启用自动着陆检测
	SignalLostLandingMaxAltFt float64 `toml:"signal_lost_landing_max_alt_ft"` // 信号丢失着陆检测的最高高度

	// 基于轨迹的阶段检测
	// 轨迹系统为每架飞行器维护一个滚动窗口的近期 ADS-B 观测,
	// 并使用统计分析(线性回归、中位数滤波)做出抗噪声的阶段判定,
	// 而不是依赖单个数据点。
	TrajectoryBufferDurationSec   int     `toml:"trajectory_buffer_duration_sec"`   // 每架飞行器的 ADS-B 历史秒数(默认:90)
	TrajectoryMinPoints           int     `toml:"trajectory_min_points"`            // 进行完整轨迹分析所需的最少有效点数(默认:5)
	TrajectoryStaleTimeoutSec     int     `toml:"trajectory_stale_timeout_sec"`     // 此时长未见到的飞行器将被移除(默认:300)
	TrajectoryCleanupIntervalSec  int     `toml:"trajectory_cleanup_interval_sec"`  // 清理 goroutine 的间隔(默认:30)
	TrajectoryDescentThresholdFPM float64 `toml:"trajectory_descent_threshold_fpm"` // 高度趋势低于此值 = 下降(默认:-200)
	TrajectoryClimbThresholdFPM   float64 `toml:"trajectory_climb_threshold_fpm"`   // 高度趋势高于此值 = 爬升(默认:200)
	TrajectoryLevelBandFt         float64 `toml:"trajectory_level_band_ft"`         // (AltMax-AltMin) 在此范围内 = 平飞(默认:200)
	TrajectoryTurningRateDeg      float64 `toml:"trajectory_turning_rate_deg"`      // |TrackRate| 高于此值 = 转弯(默认:1.5 度/秒)

	// 在用跑道检测
	// 通过观察飞行器的进近/着陆/起飞模式,确定当前哪些跑道端在使用。
	// 用于抑制垂直跑道上的虚假 APP 判定。
	RunwayInUseWindowMinutes  int     `toml:"runway_in_use_window_minutes"`  // 滚动证据窗口(默认:60)
	RunwayInUseApproachWeight float64 `toml:"runway_in_use_approach_weight"` // APP 事件的权重(默认:2.0)
	RunwayInUseLandingWeight  float64 `toml:"runway_in_use_landing_weight"`  // T/D 事件的权重(默认:3.0)
	RunwayInUseClimbWeight    float64 `toml:"runway_in_use_climb_weight"`    // CLB 事件的权重(默认:2.0)
	RunwayInUseDecayRate      float64 `toml:"runway_in_use_decay_rate"`      // 每分钟时间衰减(默认:0.98)
}

// Load 从指定文件路径加载配置
func Load(path string) (*Config, error) {
	var config Config

	// 检查文件是否存在
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("配置文件未找到:%s", path)
	}

	// 读取配置文件
	meta, err := toml.DecodeFile(path, &config)
	if err != nil {
		return nil, fmt.Errorf("解码配置文件失败:%w", err)
	}

	// 严格的 ADS-B schema:拒绝 [adsb] 下的未知键
	for _, key := range meta.Undecoded() {
		if len(key) > 0 && key[0] == "adsb" {
			return nil, fmt.Errorf("未知的 [adsb] 配置键:%s", key.String())
		}
	}

	return &config, nil
}

// LoadWithFallback 按偏好顺序检查多个位置以加载配置
func LoadWithFallback(preferredPath string) (*Config, error) {
	config, _, err := LoadWithFallbackAndPath(preferredPath)
	return config, err
}

// LoadWithFallbackAndPath 加载配置并返回解析出的配置文件路径。
func LoadWithFallbackAndPath(preferredPath string) (*Config, string, error) {
	// 按偏好顺序检查的路径列表
	searchPaths := []string{
		preferredPath,         // 用户指定的路径(如已提供)
		"configs/config.toml", // 旧版位置,位于 configs/ 文件夹
		"config.toml",         // 根目录
	}

	// 去重的同时保留顺序
	uniquePaths := make([]string, 0, len(searchPaths))
	seen := make(map[string]bool)
	for _, path := range searchPaths {
		if path != "" && !seen[path] {
			uniquePaths = append(uniquePaths, path)
			seen[path] = true
		}
	}

	var lastErr error
	for _, path := range uniquePaths {
		if _, err := os.Stat(path); err == nil {
			// 文件存在,尝试加载
			config, err := Load(path)
			if err != nil {
				lastErr = fmt.Errorf("从 %s 加载配置失败:%w", path, err)
				continue
			}

			resolvedPath := path
			if absPath, absErr := filepath.Abs(path); absErr == nil {
				resolvedPath = absPath
			}

			return config, resolvedPath, nil
		}
		lastErr = fmt.Errorf("配置文件未找到:%s", path)
	}

	return nil, "", fmt.Errorf("在任何预期位置都未找到配置文件:%v。最后的错误:%w", uniquePaths, lastErr)
}

// Validate 校验配置
func (c *Config) Validate() error {
	// 校验 frequencies 配置
	if err := c.ValidateFrequencies(); err != nil {
		return err
	}

	// 校验 post-processing 配置
	if c.PostProcessing.Enabled && c.PostProcessing.ContextTranscriptions < 0 {
		return fmt.Errorf("无效的 context_transcriptions 值:%d (必须 >= 0)", c.PostProcessing.ContextTranscriptions)
	}

	// 校验 server 配置
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("无效的服务器端口:%d", c.Server.Port)
	}
	// 校验 AdditionalPorts
	portsSeen := make(map[int]bool)
	portsSeen[c.Server.Port] = true
	for _, p := range c.Server.AdditionalPorts {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("无效的附加服务器端口:%d", p)
		}
		if portsSeen[p] {
			return fmt.Errorf("重复配置的端口:%d (主端口或附加端口)", p)
		}
		portsSeen[p] = true
	}

	// 校验硬编码的静态文件目录是否存在
	if _, err := os.Stat("www"); os.IsNotExist(err) {
		return fmt.Errorf("静态文件目录不存在:%s", "www")
	}

	// 校验 ADSB 配置
	if c.ADSB.SourceType == "" {
		return fmt.Errorf("adsb.source_type 是必填项 (必须为以下之一:external-rapidapi、external-opensky、tar1090、readsb-api、readsb-file)")
	}

	switch c.ADSB.SourceType {
	case "external-rapidapi":
		if c.ADSB.ExternalSourceURL == "" {
			return fmt.Errorf("当 source_type 为 external-rapidapi 时 external_source_url 是必填项")
		}
		if c.ADSB.APIHost == "" {
			return fmt.Errorf("当 source_type 为 external-rapidapi 时 api_host 是必填项")
		}
		if c.ADSB.APIKey == "" {
			return fmt.Errorf("当 source_type 为 external-rapidapi 时 api_key 是必填项")
		}
		if c.ADSB.SearchRadiusNM <= 0 {
			return fmt.Errorf("当 source_type 为 external-rapidapi 时 search_radius_nm 必须为正数")
		}
	case "external-opensky":
		if c.ADSB.OpenSkyBaseURL == "" {
			return fmt.Errorf("当 source_type 为 external-opensky 时 opensky_base_url 是必填项")
		}
		if c.ADSB.SearchRadiusNM <= 0 {
			return fmt.Errorf("当 source_type 为 external-opensky 时 search_radius_nm 必须为正数")
		}
		if c.ADSB.OpenSkyAuthMode == "" {
			c.ADSB.OpenSkyAuthMode = "anonymous"
		}
		switch c.ADSB.OpenSkyAuthMode {
		case "anonymous":
			// 无额外必填字段
		case "oauth2":
			if c.ADSB.OpenSkyTokenURL == "" {
				return fmt.Errorf("当 source_type 为 external-opensky 且 opensky_auth_mode 为 oauth2 时 opensky_token_url 是必填项")
			}
			if c.ADSB.OpenSkyOAuth2CredentialsPath == "" {
				return fmt.Errorf("当 source_type 为 external-opensky 且 opensky_auth_mode 为 oauth2 时 opensky_oauth2_credentials_path 是必填项")
			}
		default:
			return fmt.Errorf("无效的 opensky_auth_mode:%s (必须为以下之一:anonymous、oauth2)", c.ADSB.OpenSkyAuthMode)
		}
	case "tar1090":
		if c.ADSB.Tar1090BaseURL == "" {
			return fmt.Errorf("当 source_type 为 tar1090 时 tar1090_base_url 是必填项")
		}
	case "readsb-api":
		if c.ADSB.ReadsbAPIURL == "" {
			return fmt.Errorf("当 source_type 为 readsb-api 时 readsb_api_url 是必填项")
		}
	case "readsb-file":
		// 无强制字段。当 readsb_data_dir 为空时,自动检测使用标准文件系统路径。
	default:
		return fmt.Errorf("无效的 ADSB 数据源类型:%s (必须为以下之一:external-rapidapi、external-opensky、tar1090、readsb-api、readsb-file)", c.ADSB.SourceType)
	}

	if c.ADSB.FetchIntervalSecs <= 0 {
		return fmt.Errorf("无效的获取间隔:%d", c.ADSB.FetchIntervalSecs)
	}
	if c.Storage.DBRetentionDays <= 0 {
		c.Storage.DBRetentionDays = 7 // 默认 7 天
	}

	// 校验 logging 配置
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
		// 有效的日志级别
	default:
		return fmt.Errorf("无效的日志级别:%s", c.Logging.Level)
	}

	switch c.Logging.Format {
	case "json", "console":
		// 有效的日志格式
	default:
		return fmt.Errorf("无效的日志格式:%s", c.Logging.Format)
	}

	// 校验 storage 配置
	if c.Storage.Type != "sqlite" {
		return fmt.Errorf("无效的存储类型:%s (仅支持 'sqlite')", c.Storage.Type)
	}

	if c.Storage.Type == "sqlite" && c.Storage.SQLiteBasePath == "" {
		return fmt.Errorf("当存储类型为 sqlite 时 sqlite_base_path 是必填项")
	}

	// 校验 Station 配置
	if err := c.ValidateStation(); err != nil {
		return err
	}

	// 校验 Flight Phases 配置
	if err := c.ValidateFlightPhases(); err != nil {
		return err
	}

	// 校验 Weather 配置
	if err := c.ValidateWeather(); err != nil {
		return err
	}

	// 为已启用功能校验 OpenAI API 密钥
	if err := c.ValidateOpenAIKeys(); err != nil {
		return err
	}

	return nil
}

// ValidateStation 校验站点配置
func (c *Config) ValidateStation() error {
	// 校验纬度
	if c.Station.Latitude < -90 || c.Station.Latitude > 90 {
		return fmt.Errorf("无效的站点纬度:%f", c.Station.Latitude)
	}

	// 校验经度
	if c.Station.Longitude < -180 || c.Station.Longitude > 180 {
		return fmt.Errorf("无效的站点经度:%f", c.Station.Longitude)
	}

	// 海拔可以为负数,因此仅检查是否在合理范围内,例如 -2000 到 30000 英尺。
	if c.Station.ElevationFeet < -2000 || c.Station.ElevationFeet > 30000 {
		return fmt.Errorf("站点海拔超出典型范围:%d ft", c.Station.ElevationFeet)
	}

	// 机场代码校验现在由 ValidateWeather 方法处理

	// 默认显示范围
	if c.Station.DisplayRangeNM <= 0 {
		c.Station.DisplayRangeNM = 100.0
	}

	return nil
}

// ValidateFrequencies 校验 frequencies 配置
func (c *Config) ValidateFrequencies() error {
	// 如果未配置任何频率,则跳过校验
	if len(c.Frequencies.Sources) == 0 {
		return nil
	}

	// 校验缓冲区大小
	if c.Frequencies.BufferSizeKB <= 0 {
		return fmt.Errorf("无效的缓冲区大小:%d KB", c.Frequencies.BufferSizeKB)
	}

	// 校验重连间隔
	if c.Frequencies.ReconnectIntervalSecs <= 0 {
		return fmt.Errorf("无效的重连间隔:%d", c.Frequencies.ReconnectIntervalSecs)
	}

	// 校验 FFmpeg 超时配置
	if c.Frequencies.FFmpegTimeoutSecs < 0 {
		return fmt.Errorf("无效的 ffmpeg_timeout_secs:%d (必须 >= 0)", c.Frequencies.FFmpegTimeoutSecs)
	}
	if c.Frequencies.FFmpegReconnectDelaySecs < 0 {
		return fmt.Errorf("无效的 ffmpeg_reconnect_delay_secs:%d (必须 >= 0)", c.Frequencies.FFmpegReconnectDelaySecs)
	}

	// 当未指定时,为 FFmpeg 超时配置设置默认值
	// FFmpegTimeoutSecs 默认为 0(无超时)——无需显式设置
	if c.Frequencies.FFmpegReconnectDelaySecs == 0 {
		c.Frequencies.FFmpegReconnectDelaySecs = 2 // 默认 2 秒
	}

	// 校验频率源
	idMap := make(map[string]bool)
	orderMap := make(map[int]string) // 跟踪 order 以检查重复
	for i, freq := range c.Frequencies.Sources {
		// 校验 ID
		if freq.ID == "" {
			return fmt.Errorf("频率 #%d:ID 是必填项", i+1)
		}
		if idMap[freq.ID] {
			return fmt.Errorf("频率 #%d:重复的 ID:%s", i+1, freq.ID)
		}
		idMap[freq.ID] = true

		// 校验机场
		if freq.Airport == "" {
			return fmt.Errorf("频率 #%d:airport 是必填项", i+1)
		}

		// 校验名称
		if freq.Name == "" {
			return fmt.Errorf("频率 #%d:name 是必填项", i+1)
		}

		// 校验频率
		if freq.FrequencyMHz <= 0 {
			return fmt.Errorf("频率 #%d:无效的频率:%f", i+1, freq.FrequencyMHz)
		}

		// 校验 URL
		if freq.URL == "" {
			return fmt.Errorf("频率 #%d:URL 是必填项", i+1)
		}

		// 校验 order
		if freq.Order <= 0 {
			return fmt.Errorf("频率 #%d:order 必须为正整数", i+1)
		}
		if existingID, exists := orderMap[freq.Order]; exists {
			return fmt.Errorf("频率 #%d:重复的 order 值 %d (已被 %s 使用)", i+1, freq.Order, existingID)
		}
		orderMap[freq.Order] = freq.ID
	}

	return nil
}

// ValidateFlightPhases 校验飞行阶段配置
func (c *Config) ValidateFlightPhases() error {
	if !c.FlightPhases.Enabled {
		return nil // 如果禁用了飞行阶段则跳过校验
	}

	// 当未指定时,为新字段设置默认值
	if c.FlightPhases.FlyingMinTASKts == 0 {
		c.FlightPhases.FlyingMinTASKts = 50.0
	}
	if c.FlightPhases.FlyingMinAltFt == 0 {
		c.FlightPhases.FlyingMinAltFt = 700.0
	}
	if c.FlightPhases.HelicopterAltMultiplier == 0 {
		c.FlightPhases.HelicopterAltMultiplier = 2.0
	}
	if c.FlightPhases.HighSpeedThresholdKts == 0 {
		c.FlightPhases.HighSpeedThresholdKts = 200.0
	}
	if c.FlightPhases.PhasePreservationSeconds == 0 {
		c.FlightPhases.PhasePreservationSeconds = 60
	}
	if c.FlightPhases.PhaseTransitionTimeoutSeconds == 0 {
		c.FlightPhases.PhaseTransitionTimeoutSeconds = 60
	}
	if c.FlightPhases.HighAltitudeOverrideFt == 0 {
		c.FlightPhases.HighAltitudeOverrideFt = 5000.0
	}
	if c.FlightPhases.ImpossibleAltDropThresholdFt == 0 {
		c.FlightPhases.ImpossibleAltDropThresholdFt = 10000.0
	}
	if c.FlightPhases.ImpossibleSpeedDropThresholdKts == 0 {
		c.FlightPhases.ImpossibleSpeedDropThresholdKts = 100.0
	}
	if c.FlightPhases.ImpossibleSpeedDropMinAltFt == 0 {
		c.FlightPhases.ImpossibleSpeedDropMinAltFt = 5000.0
	}
	if c.FlightPhases.SignalLostLandingMaxAltFt == 0 {
		c.FlightPhases.SignalLostLandingMaxAltFt = 1000.0
	}
	if c.FlightPhases.ApproachMaxAltitudeFt == 0 {
		c.FlightPhases.ApproachMaxAltitudeFt = 5000
	}

	// 轨迹默认值
	if c.FlightPhases.TrajectoryBufferDurationSec == 0 {
		c.FlightPhases.TrajectoryBufferDurationSec = 90
	}
	if c.FlightPhases.TrajectoryMinPoints == 0 {
		c.FlightPhases.TrajectoryMinPoints = 5
	}
	if c.FlightPhases.TrajectoryStaleTimeoutSec == 0 {
		c.FlightPhases.TrajectoryStaleTimeoutSec = 300
	}
	if c.FlightPhases.TrajectoryCleanupIntervalSec == 0 {
		c.FlightPhases.TrajectoryCleanupIntervalSec = 30
	}
	if c.FlightPhases.TrajectoryDescentThresholdFPM == 0 {
		c.FlightPhases.TrajectoryDescentThresholdFPM = -200
	}
	if c.FlightPhases.TrajectoryClimbThresholdFPM == 0 {
		c.FlightPhases.TrajectoryClimbThresholdFPM = 200
	}
	if c.FlightPhases.TrajectoryLevelBandFt == 0 {
		c.FlightPhases.TrajectoryLevelBandFt = 200
	}
	if c.FlightPhases.TrajectoryTurningRateDeg == 0 {
		c.FlightPhases.TrajectoryTurningRateDeg = 1.5
	}

	// 在用跑道默认值
	if c.FlightPhases.RunwayInUseWindowMinutes == 0 {
		c.FlightPhases.RunwayInUseWindowMinutes = 60
	}
	if c.FlightPhases.RunwayInUseApproachWeight == 0 {
		c.FlightPhases.RunwayInUseApproachWeight = 5.0
	}
	if c.FlightPhases.RunwayInUseLandingWeight == 0 {
		c.FlightPhases.RunwayInUseLandingWeight = 3.0
	}
	if c.FlightPhases.RunwayInUseClimbWeight == 0 {
		c.FlightPhases.RunwayInUseClimbWeight = 1.5
	}
	if c.FlightPhases.RunwayInUseDecayRate == 0 {
		c.FlightPhases.RunwayInUseDecayRate = 0.98
	}

	// 校验高度阈值
	if c.FlightPhases.CruiseAltitudeFt <= 0 {
		return fmt.Errorf("cruise_altitude_ft 必须为正数:%d", c.FlightPhases.CruiseAltitudeFt)
	}
	if c.FlightPhases.DepartureAltitudeFt <= 0 {
		return fmt.Errorf("departure_altitude_ft 必须为正数:%d", c.FlightPhases.DepartureAltitudeFt)
	}

	// 校验速度阈值
	if c.FlightPhases.TaxiingMinSpeedKts < 0 {
		return fmt.Errorf("taxiing_min_speed_kts 必须为非负数:%d", c.FlightPhases.TaxiingMinSpeedKts)
	}
	if c.FlightPhases.TaxiingMaxSpeedKts <= c.FlightPhases.TaxiingMinSpeedKts {
		return fmt.Errorf("taxiing_max_speed_kts (%d) 必须大于 taxiing_min_speed_kts (%d)",
			c.FlightPhases.TaxiingMaxSpeedKts, c.FlightPhases.TaxiingMinSpeedKts)
	}

	// 校验进近检测参数
	if c.FlightPhases.ApproachCenterlineToleranceNM <= 0 {
		return fmt.Errorf("approach_centerline_tolerance_nm 必须为正数:%f", c.FlightPhases.ApproachCenterlineToleranceNM)
	}
	if c.FlightPhases.ApproachMaxDistanceNM <= 0 {
		return fmt.Errorf("approach_max_distance_nm 必须为正数:%d", c.FlightPhases.ApproachMaxDistanceNM)
	}
	if c.FlightPhases.ApproachHeadingToleranceDeg <= 0 || c.FlightPhases.ApproachHeadingToleranceDeg > 180 {
		return fmt.Errorf("approach_heading_tolerance_deg 必须在 1 到 180 之间:%f", c.FlightPhases.ApproachHeadingToleranceDeg)
	}

	// 校验新的地面检测阈值
	if c.FlightPhases.FlyingMinTASKts <= 0 {
		return fmt.Errorf("flying_min_tas_kts 必须为正数:%f", c.FlightPhases.FlyingMinTASKts)
	}
	if c.FlightPhases.FlyingMinAltFt <= 0 {
		return fmt.Errorf("flying_min_alt_ft 必须为正数:%f", c.FlightPhases.FlyingMinAltFt)
	}
	if c.FlightPhases.HelicopterAltMultiplier <= 0 {
		return fmt.Errorf("helicopter_alt_multiplier 必须为正数:%f", c.FlightPhases.HelicopterAltMultiplier)
	}
	if c.FlightPhases.HighSpeedThresholdKts <= 0 {
		return fmt.Errorf("high_speed_threshold_kts 必须为正数:%f", c.FlightPhases.HighSpeedThresholdKts)
	}

	// 校验阶段保留时间
	if c.FlightPhases.PhasePreservationSeconds <= 0 {
		return fmt.Errorf("phase_preservation_seconds 必须为正数:%d", c.FlightPhases.PhasePreservationSeconds)
	}
	if c.FlightPhases.PhaseTransitionTimeoutSeconds <= 0 {
		return fmt.Errorf("phase_transition_timeout_seconds 必须为正数:%d", c.FlightPhases.PhaseTransitionTimeoutSeconds)
	}
	// 校验信号丢失着陆检测
	if c.FlightPhases.SignalLostLandingEnabled && c.FlightPhases.SignalLostLandingMaxAltFt <= 0 {
		return fmt.Errorf("当 signal_lost_landing_enabled 为 true 时 signal_lost_landing_max_alt_ft 必须为正数:%f", c.FlightPhases.SignalLostLandingMaxAltFt)
	}

	return nil
}

// ValidateOpenAIKeys 为已启用功能校验 OpenAI API 密钥
func (c *Config) ValidateOpenAIKeys() error {
	// 检查转写 API 密钥——只要配置了,转写功能始终可用
	if c.Transcription.OpenAIAPIKey == "" {
		fmt.Printf("WARN: 未提供用于转写的 OpenAI API 密钥——转写功能将被禁用\n")
	}

	// 如果启用了 ATC chat,则检查其 API 密钥
	if c.ATCChat.Enabled && c.ATCChat.OpenAIAPIKey == "" {
		fmt.Printf("WARN: ATC Chat 已启用但未提供 OpenAI API 密钥——ATC chat 功能将被禁用\n")
	}

	// 如果启用了后处理,则检查其 API 密钥
	if c.PostProcessing.Enabled {
		// 后处理使用与转写相同的 API 密钥
		if c.Transcription.OpenAIAPIKey == "" {
			fmt.Printf("WARN: 后处理已启用但 transcription 配置中未提供 OpenAI API 密钥——后处理功能将被禁用\n")
		}
	}

	return nil
}

// ValidateWeather 校验气象配置
func (c *Config) ValidateWeather() error {
	// 校验刷新间隔
	if c.Weather.RefreshIntervalMinutes <= 0 {
		return fmt.Errorf("气象 refresh_interval_minutes 必须大于 0:%d", c.Weather.RefreshIntervalMinutes)
	}

	// 校验请求超时
	if c.Weather.RequestTimeoutSeconds <= 0 {
		return fmt.Errorf("气象 request_timeout_seconds 必须大于 0:%d", c.Weather.RequestTimeoutSeconds)
	}

	// 校验最大重试次数
	if c.Weather.MaxRetries < 0 {
		return fmt.Errorf("气象 max_retries 必须为 0 或更大:%d", c.Weather.MaxRetries)
	}

	// 校验缓存过期时间
	if c.Weather.CacheExpiryMinutes <= 0 {
		return fmt.Errorf("气象 cache_expiry_minutes 必须大于 0:%d", c.Weather.CacheExpiryMinutes)
	}

	// 校验 API 基础 URL
	if c.Weather.APIBaseURL == "" {
		return fmt.Errorf("气象 api_base_url 不能为空")
	}

	// 至少需启用一种气象类型
	if !c.Weather.FetchMETAR && !c.Weather.FetchTAF && !c.Weather.FetchNOTAMs {
		return fmt.Errorf("至少需要启用一种气象类型 (fetch_metar、fetch_taf 或 fetch_notams)")
	}

	// 校验当启用气象获取时机场代码已设置
	if (c.Weather.FetchMETAR || c.Weather.FetchTAF || c.Weather.FetchNOTAMs) && c.Station.AirportCode == "" {
		return fmt.Errorf("当启用气象获取时 station airport_code 是必填项")
	}

	return nil
}

// WeatherConfig 包含气象数据获取与缓存配置
type WeatherConfig struct {
	RefreshIntervalMinutes int    `toml:"refresh_interval_minutes"` // 气象数据刷新间隔(分钟)
	APIBaseURL             string `toml:"api_base_url"`             // 气象 API 的基础 URL(例如 https://node.windy.com/airports)
	RequestTimeoutSeconds  int    `toml:"request_timeout_seconds"`  // HTTP 请求超时(秒)
	MaxRetries             int    `toml:"max_retries"`              // 失败请求的最大重试次数
	FetchMETAR             bool   `toml:"fetch_metar"`              // 是否获取 METAR 数据
	FetchTAF               bool   `toml:"fetch_taf"`                // 是否获取 TAF 数据
	FetchNOTAMs            bool   `toml:"fetch_notams"`             // 是否获取 NOTAM 数据
	CacheExpiryMinutes     int    `toml:"cache_expiry_minutes"`     // 刷新失败时缓存数据的保留时长
}

// ATCChatConfig 包含 ATC Chat 语音助手配置
type ATCChatConfig struct {
	// 功能开关
	Enabled bool `toml:"enabled"` // 启用或禁用 ATC Chat 功能

	// OpenAI API 设置
	OpenAIAPIKey  string `toml:"openai_api_key"` // 用于实时聊天的 OpenAI API 密钥
	RealtimeModel string `toml:"realtime_model"` // 使用的 OpenAI realtime 模型
	Voice         string `toml:"voice"`          // 音频回复使用的语音

	// 音频设置
	InputAudioFormat  string `toml:"input_audio_format"`  // 输入音频格式(例如 "pcm16")
	OutputAudioFormat string `toml:"output_audio_format"` // 输出音频格式(例如 "pcm16")
	SampleRate        int    `toml:"sample_rate"`         // 音频采样率(Hz)
	Channels          int    `toml:"channels"`            // 音频声道数

	// 会话设置
	MaxResponseTokens int     `toml:"max_response_tokens"` // 回复中的最大 token 数
	Temperature       float64 `toml:"temperature"`         // 回复随机性(0.0-1.0)
	TurnDetectionType string  `toml:"turn_detection_type"` // 轮次检测方法
	VADThreshold      float64 `toml:"vad_threshold"`       // 语音活动检测阈值
	SilenceDurationMs int     `toml:"silence_duration_ms"` // 用于轮次检测的静音时长

	// 上下文设置
	MaxContextAircraft          int `toml:"max_context_aircraft"`          // 上下文中包含的最大飞行器数量
	TranscriptionHistorySeconds int `toml:"transcription_history_seconds"` // 包含的转写历史秒数

	// 系统提示词配置
	SystemPromptPath        string `toml:"system_prompt_path"`    // 系统提示词模板文件路径
	RefreshSystemPromptSecs int    `toml:"refresh_system_prompt"` // 系统提示词自动刷新间隔(秒,0 = 禁用)
}
