# CLAUDE.md - Co-ATC 项目指南

## 项目概述

Co-ATC 是一套面向空中交通管制(ATC)运行的 AI 增强飞行器监控系统。它提供实时空域监控、AI 语音助手、ATC 通信转写以及智能告警系统。**仅供本地/局域网使用 —— 不可暴露至公网。**

## 快速参考

### 构建与运行

```powershell
# 构建(Windows——始终使用此方式,而不是 'go run')
.\build_windows.ps1

# 运行
.\bin\co-atc.exe

# 使用自定义配置
.\bin\co-atc.exe -config configs/config.toml
```

```bash
# 构建(macOS)
./build_mac.sh

# 构建(Linux)
./build_linux.sh

# 运行
./bin/co-atc
```

### 关键 URL(默认)

- Web UI:`http://localhost:8080`
- API:`http://localhost:8080/api/v1/`
- WebSocket:`ws://localhost:8080/ws`

## 技术栈

**后端:**Go 1.23.2、chi/v5 路由、SQLite(纯 Go)、Gorilla WebSocket、zap 日志、TOML 配置

**前端:**Alpine.js、Leaflet.js、Tailwind CSS、Web Audio API

**AI:**OpenAI Whisper(转写)、GPT-4(后处理)、Realtime API(语音助手)

## 项目结构

```
cmd/server/main.go          # 程序入口
internal/
  adsb/                     # 飞行器追踪(ADS-B 数据)
  api/                      # HTTP 路由与处理器
  audio/                    # 音频处理(SRT、FFmpeg)
  atcchat/                  # 语音助手(OpenAI Realtime)
  config/                   # TOML 配置
  frequencies/              # 无线电频率管理
  simulation/               # 模拟飞行器
  storage/sqlite/           # 数据库层
  templating/               # AI 上下文聚合
  transcription/            # Whisper 转写
  weather/                  # METAR/TAF/NOTAM
  websocket/                # 实时广播
www/                        # 前端(Alpine.js SPA)
assets/                     # 静态数据(航司、机场、跑道)
prompts/                    # AI 系统提示词
configs/config.toml         # 配置文件
data/                       # SQLite 数据库(自动创建)
docs/                       # 文档
```

## 关键模式

### 服务架构
- 每个领域都是一个自包含的服务,采用依赖注入
- 上下文感知,支持优雅停机
- 实时操作使用并发 goroutine

### 实时数据流
1. ADS-B 客户端 → 服务 → 变更检测 → WebSocket → 前端
2. 音频流 → 转写 → OpenAI Whisper → SQLite → WebSocket

### 飞行阶段
10 阶段体系:NEW(新建)、TAX(滑行)、T/O(起飞)、CLB(爬升)、DEP(离场)、CRZ(巡航)、ARR(进场)、APP(进近)、T/D(着陆)、UNK(未知)

### 数据库
- 每日轮换:`data/co-atc-YYYY-MM-DD.db`
- 纯 Go SQLite(无 CGO)

## 开发指引

### 后端
- 使用 PowerShell 终端命令(Windows 开发环境)
- 通过 `build_windows.ps1` 构建,严禁使用 `go run`
- 修改 API 时同步更新 `docs/api_spec.md`
- 清理重复或失效的代码

### 前端
- 优先使用 Tailwind CSS 类,而非自定义 CSS
- 遵循 Alpine.js 模式
- 深色主题,绿色高亮
- 仅前端改动无需重新构建

### 文档
- 把 `docs/project_progress.md` 当作工作记忆持续维护
- 重大功能更新后同步更新 README.md

## 重要文件

- `configs/config.toml.example` - 带文档说明的配置模板
- `docs/api_spec.md` - API 端点文档
- `docs/technical_docs.md` - 架构详情
- `.roorules` - 项目规范
- `prompts/*.txt` - AI 系统提示词

## 测试

无自动化测试套件。需手动测试:
- API:使用 curl/Postman 调用接口
- 前端:浏览器测试
- 启动时会自动校验配置

## 备注

- **无认证机制** - 开发/兴趣项目
- AI 功能需要在配置中提供 OpenAI API 密钥
- 前端从 `www/` 目录提供
- WebSocket 变更检测可减少约 95% 的带宽
