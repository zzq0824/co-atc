# Co-ATC 技术文档

本文档全面介绍 Co-ATC 的系统架构、实现细节及内部工作原理。

## 系统架构

Co-ATC 由基于 Go 的后端服务器与 Web 前端共同构成,旨在面向实时空中交通监控与 AI 增强运行。

### 核心组件

1. **后端服务器**(`cmd/server/main.go`)
   - 基于 Go 的 HTTP/WebSocket 服务器
   - 支持多端口配置
   - 优雅停机并妥善清理资源

2. **前端**(`www/`)
   - HTML5/CSS3/JavaScript Web 应用
   - 使用 Alpine.js 构建响应式 UI 组件
   - 使用 OpenLayers 实现交互式地图
   - 通过 WebSocket 进行实时通信

3. **数据存储**
   - 使用纯 Go 驱动的 SQLite 数据库(modernc.org/sqlite)
   - 每日数据库文件:`co-atc-YYYY-MM-DD.db`
   - 无需任何 CGO 依赖

## 项目结构

```
co-atc/
├── cmd/                      # 应用程序入口
│   └── server/               # 主服务程序
├── internal/                 # 私有应用代码
│   ├── adsb/                 # ADS-B 数据处理
│   │   ├── client.go         # 用于获取 ADS-B 数据的客户端
│   │   ├── atc_utils.go      # 航空相关工具与计算
│   │   ├── external.go       # 外部 ADS-B API 集成
│   │   ├── models.go         # ADS-B 数据模型
│   │   ├── service.go        # ADS-B 服务实现
│   │   ├── change_detector.go # 飞行器变更检测
│   │   └── websocket_handler.go # WebSocket 消息处理
│   ├── api/                  # API 处理器与路由
│   │   ├── handlers.go       # API 请求处理器
│   │   ├── middleware.go     # API 中间件
│   │   ├── routes.go         # API 路由定义
│   │   ├── static.go         # 静态文件服务
│   │   ├── atc_chat_handlers.go # ATC chat API 处理器
│   │   └── transcription_handlers.go # 转写处理器
│   ├── atcchat/              # ATC Chat AI 助手
│   │   ├── models.go         # 聊天数据模型
│   │   ├── realtime_client.go # OpenAI realtime API 客户端
│   │   └── service.go        # 聊天服务实现
│   ├── audio/                # 音频处理
│   │   ├── central_processor.go # 统一音频处理
│   │   ├── chunker.go        # 用于转写的音频分块
│   │   ├── multireader.go    # 多读取者支持
│   │   └── wavreader.go      # WAV 格式处理
│   ├── config/               # 配置处理
│   │   └── config.go         # 配置加载与验证
│   ├── frequencies/          # 频率管理
│   │   ├── client.go         # 音频流客户端
│   │   ├── models.go         # 频率数据模型
│   │   └── service.go        # 频率服务实现
│   ├── simulation/           # 飞行器仿真
│   │   └── service.go        # 仿真服务实现
│   ├── storage/              # 数据存储实现
│   │   └── sqlite/           # SQLite 存储
│   │       ├── aircraft.go   # 飞行器数据存储
│   │       ├── clearances.go # ATC 许可存储
│   │       ├── clearance_models.go # 许可数据模型
│   │       └── transcriptions.go # 转写存储
│   ├── templating/           # 模板系统
│   │   ├── aggregator.go     # 数据聚合
│   │   ├── engine.go         # 模板引擎
│   │   ├── formatters.go     # 数据格式化
│   │   ├── models.go         # 模板模型
│   │   └── templating.go     # 模板工具
│   ├── transcription/        # 音频转写
│   │   ├── interface.go      # 转写接口
│   │   ├── manager.go        # 转写管理
│   │   ├── models.go         # 转写数据模型
│   │   ├── openai.go         # OpenAI API 集成
│   │   ├── post_processor.go # 基于 LLM 的后处理
│   │   └── processor.go      # 转写处理
│   ├── weather/              # 气象数据集成
│   │   ├── cache.go          # 气象数据缓存
│   │   ├── client.go         # 气象 API 客户端
│   │   ├── models.go         # 气象数据模型
│   │   └── service.go        # 气象服务实现
│   └── websocket/            # WebSocket 服务器
│       └── server.go         # WebSocket 服务器实现
├── assets/                   # 静态数据文件
│   ├── aircraft.csv          # 飞行器数据增强
│   ├── airlines.dat          # 航司参考数据
│   ├── airports.csv          # 机场参考数据
│   ├── airport-frequencies.csv # 机场频率参考数据
│   ├── runways.csv           # 跑道参考数据
│   └── navaids.csv           # 导航台参考数据
├── prompts/                  # AI 系统提示词
│   ├── atc_chat_prompt.txt   # ATC 语音助手提示词
│   ├── post_processing_prompt.txt # 后处理提示词
│   └── transcription_prompt.txt # 转写提示词
├── configs/                  # 配置文件
│   └── config.toml           # 主配置文件
└── www/                      # 前端 Web 应用
    ├── index.html            # 主 HTML 页面
    ├── style.css             # CSS 样式
    ├── app.js                # 主应用逻辑
    ├── map/                  # OpenLayers 地图模块(core/renderers/features/perf)
    ├── map-manager.js        # 兼容性垫片
    ├── websocket-client.js   # WebSocket 客户端
    ├── aircraft-animation.js # 飞行器动画引擎
    ├── atc-chat.js           # ATC 聊天界面
    ├── audio-client.js       # 音频流客户端
    └── sounds/               # 音频资源
```

## 后台工作者与 Goroutine

Co-ATC 使用多个后台工作者与 goroutine 高效处理并发操作:

### 1. WebSocket 服务器
- **位置**:`internal/websocket/server.go`
- **目的**:管理与客户端的实时通信
- **工作者**:
  - WebSocket 主循环:负责客户端注册、注销与消息广播
  - 每客户端读 goroutine:检测客户端断开
  - 每客户端写 goroutine:向已连接客户端发送消息

### 2. ADS-B 服务
- **位置**:`internal/adsb/service.go`
- **目的**:处理飞行器追踪数据
- **工作者**:
  - fetchLoop:按配置间隔周期性获取并处理 ADS-B 数据
  - 支持的数据源模式:`tar1090`、`readsb-api`、`readsb-file`、`external-rapidapi`、`external-opensky`
  - 启动时校验所配置的数据源(若数据源不可访问/无效则快速失败)
  - 检测飞行器起飞与着陆
  - 基于进近/着陆/离场证据进行活跃跑道使用检测
  - 更新飞行器状态(active、stale、signal_lost)
  - 通过 WebSocket 广播飞行器事件

### 3. 频率服务
- **位置**:`internal/frequencies/service.go`
- **目的**:管理无线电频率音频流
- **工作者**:
  - 每个频率的 StreamProcessor:为每个配置的频率管理音频流
  - cleanupInactiveClients:周期性检查并清理不活跃客户端(每 30 秒一次)
  - 并行停机:停机过程中通过 goroutine 并发停止流处理器

### 4. 音频处理系统
- **位置**:`internal/audio/central_processor.go`、`internal/audio/multireader.go`、`internal/audio/wavreader.go`
- **目的**:处理来自音频源的音频流
- **组件**:
  - **FFmpeg 管理器(CentralAudioProcessor)**:
    - processFFmpegOutput:从 ffmpeg 读取音频数据并写入 MultiReader
    - startMonitoring:监控 ffmpeg 进程健康状况(每 5 秒一次)
    - 重连定时器:在故障后按配置延迟自动重启 ffmpeg
  - **流管理器(MultiReader)**:
    - 通过环形缓冲区高效共享音频数据
    - 管理来自单一音频源的多个并发读取者
    - 每读取者使用 goroutine,借助条件变量等待新数据
    - 处理慢速客户端的反压,不影响其它客户端
  - **WAV 头生成器(WAVReader)**:
    - 动态生成 WAV 头以兼容浏览器
    - 确保 Web 客户端获得正确的音频格式

### 5. 转写系统
- **位置**:`internal/transcription/processor.go`、`internal/transcription/manager.go`
- **目的**:实时转写 ATC 通信
- **工作者**:
  - processAudio:读取音频数据,分块后发送至 OpenAI
  - processTranscriptions:接收并处理来自 OpenAI 的转写事件
  - 在与 OpenAI 服务连接失败时执行重连

### 6. 后处理
- **位置**:`internal/transcription/post_processor.go`
- **目的**:使用 LLM 处理增强原始转写
- **工作者**:
  - 后台处理循环:周期性批量处理未处理的转写
  - 使用 OpenAI 进行说话人识别、内容清理、呼号提取
  - 将数据库中的活跃飞行器数据作为上下文以提升处理质量
  - 通过 WebSocket 广播处理后的转写

### 7. HTTP 服务器
- **位置**:`cmd/server/main.go`
- **目的**:提供 API 端点与静态内容
- **工作者**:
  - 多个 HTTP 服务器:每个所配置端口对应一个 goroutine
  - 并行停机:通过 goroutine 同时停止 HTTP 服务器,带有超时控制

### 8. 优雅停机
- **位置**:`cmd/server/main.go`
- **目的**:确保应用干净退出
- **流程**:
  1. 捕获中断信号(SIGINT、SIGTERM)
  2. 按顺序停止后台服务:频率 → 转写 → ADS-B
  3. 取消主上下文,通知所有 goroutine 停止
  4. 带超时关闭 HTTP 服务器
  5. 关闭数据库连接

## 关键技术组件

### 1. 音频处理系统
- `audio/central_processor.go`:为每个频率管理一个 ffmpeg 进程
- `audio/multireader.go`:允许多个客户端从同一音频流读取
- `audio/wavreader.go`:处理 WAV 头生成以兼容浏览器
- `audio/chunker.go`:处理用于转写的音频分块

### 2. 转写系统
- `transcription/processor.go`:处理待转写的音频
- `transcription/openai.go`:集成 OpenAI 实时转写 API
- `transcription/manager.go`:管理频率级别的转写处理器

### 3. 频率管理
- `frequencies/service.go`:为不同频率管理音频流
- `frequencies/client.go`:处理与音频源的连接

### 4. ADS-B 数据处理
- `adsb/service.go`:管理 ADS-B 数据处理并处理原始数据
- `adsb/atc_utils.go`:提供航空相关工具与计算
- `adsb/external.go`:处理外部 ADS-B API 集成
- `adsb/models.go`:定义优化过的数据模型,包含去重

### 5. API 与 WebSocket
- `api/routes.go`:定义 API 端点
- `websocket/server.go`:实现 WebSocket 服务器以提供实时更新

### 6. 错误处理系统
- 应用内全面的健壮错误处理,提升可靠性
- WebSocket 重连采用指数退避
- API 请求重试,参数可配置
- 在外部服务不可用时优雅降级
- API 响应中提供结构化错误报告

## 数据库 schema

### Aircraft 表
- 存储飞行器位置与遥测数据
- 通过组合索引优化性能
- 同时支持真实与模拟飞行器(`type` 字段)

### ADS-B Targets 表
- 原始 ADS-B 数据存储,带去重
- 复合索引 (aircraft_hex, timestamp DESC)
- 存储位置、高度、速度与航向数据

### Phase Changes 表
- 跟踪飞行阶段切换(NEW、TAX、T/O、DEP、CRZ、ARR、APP、T/D)
- 反抖动逻辑防止阶段快速跳变
- 检测参数可配置

### Transcriptions 表
- 存储原始与处理后的转写数据
- 关联频率信息
- 支持后处理流程

### Clearances 表
- 存储提取出的 ATC 许可
- 关联转写来源
- 支持起飞、着陆、进近三类许可

## WebSocket 通信

### 消息类型
- `aircraft_added`:检测到新飞行器
- `aircraft_update`:飞行器数据已变更
- `aircraft_removed`:飞行器不再被追踪
- `aircraft_bulk_data`:初始数据加载
- `phase_change`:飞行阶段切换
- `clearance_issued`:提取到 ATC 许可
- `filter_update`:客户端筛选偏好

### 客户端筛选
- 服务端依据客户端偏好执行筛选
- 仅推送相关更新以降低带宽
- 支持空中/地面范围、阶段过滤与高度区间

## AI 集成

### ATC Chat 助手
- 集成 OpenAI Realtime API
- 通过按住对讲(Push-to-Talk)进行语音交互
- 实时空域上下文更新
- 模板化系统提示词,带实时数据

### 后处理
- 基于 LLM 的转写增强
- 说话人识别与呼号提取
- ATC 许可检测与分类

### 模板系统
- 面向 AI 交互的统一数据格式化
- 实时飞行器、气象与跑道数据
- 在所有 AI 服务中保持上下文一致

## 性能优化

### WebSocket 优化
- 服务端筛选降低客户端带宽
- 智能变更检测避免不必要的更新
- 地图渲染的视口剔除
- 消息批量处理

### 数据库优化
- 通过组合索引提升查询性能
- 阶段数据检索的批量操作
- 连接池与预编译语句

### 前端优化
- 通过向量外推实现飞行器动画
- 平滑位置插值
- 自适应性能监控
- 大数据集的内存管理

## 配置系统

应用使用带完整文档的 TOML 配置:
- 服务器设置(端口、超时、静态文件)
- ADS-B 数据源与处理参数
- 音频流配置
- AI 服务集成设置
- 数据库与存储选项
- 气象数据集成
- 飞行阶段检测参数

## 安全特性

### 静态文件服务
- 防御目录穿越
- 文件校验与安全检查
- 服务目录可配置

### API 安全
- 输入校验与清洗
- 结构化错误响应
- 限流能力

### WebSocket 安全
- 连接校验
- 消息类型验证
- 客户端状态管理

这一架构使 Co-ATC 能够高效处理多种并发操作,包括实时数据处理、音频流与客户端通信,同时保持干净的停机流程,避免资源泄漏。
