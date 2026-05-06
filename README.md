# Co-ATC：飞行器监控系统

Co-ATC 是一个由 AI 增强的系统,旨在监控空域活动,辅助(模拟)空中交通管制(ATC)运行。它整合了实时 ADS-B 数据(本地或远程)、ATC 通信音频流(本地 VHF 无线电或 LiveATC),同时利用 AI 转写并解读通信内容、跟踪 ATC 指令,并对潜在冲突或不合规行为发出告警。

![Co-ATC 主界面](docs/main_screen.png)

![Co-ATC 主界面 - 飞行器信息](docs/main_screen2.png)

![Co-ATC 主界面 - 邻近告警](docs/main_screen3.png)

![Co-ATC 主界面 - AI 智能 ATC](docs/main_screen4.png)

![Co-ATC 主界面 - 地图样式](docs/main_screen5.png)

## Co-ATC 的功能

Co-ATC 为空中交通管制员和航空爱好者提供:

- **实时飞行器追踪**:实时显示飞行器位置、飞行轨迹和遥测数据
- **交互式地图界面**:全方位空域视图,包含飞行器详情、天气叠加层和跑道信息
- **使用本地数据源**:连接您的 ADS-B 和 [VHF 频段](https://github.com/rtl-airband/RTLSDR-Airband/pull/523) SDR 实现基本本地(离线)追踪
- **AI 语音助手**:基于语音的 ATC 助手,具备全面的空域知识和实时上下文(需要 OpenAI API 密钥)
- **音频转写**:使用 AI 实时转写并分析 ATC 通信(需要 OpenAI API 密钥)
- **飞行阶段检测**:自动检测和跟踪飞行器各飞行阶段(滑行、起飞、离场、巡航、进场、进近、着陆)
- **ATC 许可提取**:由 AI 驱动地提取并跟踪起飞、着陆和进近许可(需要 OpenAI API 密钥)
- **飞行器仿真**:创建并控制模拟飞行器,用于训练和测试场景
- **气象集成**:实时整合 METAR、TAF 和 NOTAM 数据(借用了 Windy 的 API,抱歉!)
- **告警系统**:针对飞行器状态变化和潜在问题发出实时通知(尚未完成)

## 当前状态

Co-ATC 处于半活跃开发阶段,核心功能已实现并可正常运行。系统能够成功处理实时 ADS-B 数据、提供交互式地图可视化、转写 ATC 通信内容,并提供 AI 辅助(机场咨询服务)。

详细的进度和实现细节请参阅 [项目说明与进度](docs/project_progress.md)。

### ⚠️ 安全警告

**请勿将本应用暴露至互联网**

本应用仅设计用于本地使用,不应被互联网公开访问。它具有以下特性:

- **无身份认证系统** - 任何具有访问权限的人都可使用所有功能
- **无授权控制** - 所有功能对任何用户开放
- **无安全加固** - 仅面向开发和本地使用
- **AI 生成代码库** - 未经过专业安全审查或测试

## 系统要求

- **Go 1.21 或更高版本** - 用于构建项目
- **ADS-B 数据源** - 可访问 ADS-B 数据(例如本地 `tar1090` 服务器或外部 API)
- **FFmpeg** - 用于无线电频率音频流处理(参见下文安装说明)
- **现代浏览器** - Chrome、Firefox、Safari 或 Edge,用于访问 Web 界面
- **OpenAI API 密钥** - 仅 AI 咨询、无线电转写和许可提取功能需要

### 安装 FFmpeg

#### Windows
1. **使用 Chocolatey**(推荐):
   ```powershell
   # 如果尚未安装 Chocolatey,先安装
   Set-ExecutionPolicy Bypass -Scope Process -Force; [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol -bor 3072; iex ((New-Object System.Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))
   
   # 安装 FFmpeg
   choco install ffmpeg
   ```

2. **手动安装**:
   - 从 [https://ffmpeg.org/download.html#build-windows](https://ffmpeg.org/download.html#build-windows) 下载 FFmpeg
   - 将压缩包解压到 `C:\ffmpeg`
   - 将 `C:\ffmpeg\bin` 添加到系统 PATH 环境变量
   - 重启命令提示符或 PowerShell

#### Mac
1. **使用 Homebrew**(推荐):
   ```bash
   # 如果尚未安装 Homebrew,先安装
   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
   
   # 安装 FFmpeg
   brew install ffmpeg
   ```

2. **使用 MacPorts**:
   ```bash
   sudo port install ffmpeg
   ```

#### 验证安装
安装完成后,验证 FFmpeg 是否正常工作:
```bash
ffmpeg -version
```

## 安装与配置

### 方案 1:下载预编译二进制

获取 Co-ATC 最快的方式是从发布页面下载预编译的二进制文件。

1. **从 [GitHub Releases](https://github.com/yegors/co-atc/releases) 下载最新版本**
2. **克隆仓库**(资源文件和 www 目录所必需):
   ```bash
   git clone https://github.com/yegors/co-atc.git
   cd co-atc
   ```
3. **将二进制文件解压到项目根目录**
4. **进入下文的「配置」一节继续操作**

### 方案 2:从源码构建

如需从源码构建或修改代码:

#### Windows
```powershell
# 克隆仓库
git clone https://github.com/yegors/co-atc.git
cd co-atc

# 安装依赖
go mod download

# 使用构建脚本编译应用
.\build_windows.ps1
```

#### Mac
```bash
# 克隆仓库
git clone https://github.com/yegors/co-atc.git
cd co-atc

# 安装依赖
go mod download

# 使用构建脚本编译应用(macOS)
./build_mac.sh
```

#### Linux
```bash
# 克隆仓库
git clone https://github.com/yegors/co-atc.git
cd co-atc

# 安装依赖
go mod download

# 使用构建脚本编译应用(自动检测架构)
chmod +x ./build_linux.sh
./build_linux.sh
```

### 2. 配置

复制示例配置文件并根据您的环境进行自定义:

#### Windows
```powershell
# 复制示例配置
copy configs\config.toml.example configs\config.toml

# 编辑配置文件
notepad configs\config.toml
```

#### Mac/Linux
```bash
# 复制示例配置
cp configs/config.toml.example configs/config.toml

# 编辑配置文件
nano configs/config.toml
```

#### 关键配置项

**必填项:**
- `[adsb].source_type` - 选择数据源模式:
   - `tar1090` - 提供 `aircraft.json`、`receiver.json`、`stats.json` 的基础 URL
   - `readsb-api` - readsb HTTP API 端点(例如 `http://host:30152/?all`)
   - `readsb-file` - 本地 readsb 运行时文件(自动检测,可选用 `readsb_data_dir` 覆盖)
   - `external-rapidapi` - 使用 `external_source_url` + API 主机/密钥的外部 ADS-B API
   - `external-opensky` - OpenSky `/states/all`,可选使用 OAuth2 客户端凭证

- 当 `external-opensky` + OAuth2 时:
   - 设置 `opensky_auth_mode = "oauth2"`
   - 设置 `opensky_oauth2_credentials_path` 指向格式为 `{"clientId":"...","clientSecret":"..."}` 的 JSON 文件

**可选但推荐:**
- `[station]` - 配置您的机场/电台位置(以多伦多 CYYZ 为例)
- `[[frequencies.sources]]` - 添加用于转写的本地无线电频率(以多伦多为例)
- `transcription.openai_api_key` - 启用 AI 转写功能(若未提供则禁用)
- `atc_chat.openai_api_key` - 启用 AI 语音助手(若未提供则禁用)

配置文件中包含针对所有设置项的全面说明文档,并以多伦多皮尔逊国际机场(CYYZ)为示例。您可以将其作为模板,改为您自己的位置和频率。

**注意**:如果未提供 OpenAI API 密钥,应用仍可成功启动,但 AI 相关功能(转写、后处理、语音助手)将被禁用。启动期间会显示警告消息以提示哪些功能不可用。

### 3. 运行应用

```powershell
# 运行已编译的可执行文件
.\bin\co-atc.exe
```

应用将会:
- 启动 Web 服务器(默认:http://localhost:8080)
- 开始处理 ADS-B 数据
- 初始化音频流和转写服务
- 自动创建每日 SQLite 数据库文件

### 4. 访问界面

打开浏览器并访问 `http://localhost:8080` 即可使用 Co-ATC 界面。

## 主要功能

### 交互式地图(OpenLayers)
- 基于 OpenLayers 的地图引擎,支持实时飞行器渲染
- `www/map/` 下的模块化地图架构(core、renderers、features、telemetry)
- 飞行器、轨迹、标签、选中/悬停、邻近告警和小地图
- 底图/航图样式:深色、浅色、OpenStreetMap、VFR 区域图、终端图、IFR 低空、IFR 高空
- 支持的叠加层/图层:机场、跑道、导航台、距离环、气象雷达/云图、空域叠加和航图叠加
- 地图内提供图层切换/透明度以及飞行器显示行为的控制
- 叠加层故障隔离,在高飞行器负载下仍保持稳定

### 飞行器监控与 ADS-B 数据源
- 实时飞行器遥测数据,包含历史、回算和未来轨迹
- 飞行阶段跟踪,支持基于轨迹的状态切换以及信号丢失处理
- 基于实时进近/着陆/离场证据的活跃跑道检测
- 通过本地数据集进行数据增强(`assets/aircraft.csv`、`assets/airlines.dat`、`assets/airports.csv`、`assets/runways.csv`、`assets/navaids.csv`)
- 多种数据源模式:`tar1090`、`readsb-api`、`readsb-file`、`external-rapidapi`、`external-opensky`

### AI + 音频工作流
- 多频率 ATC 流接入与低延迟浏览器音频投放
- 实时转写 + 可选的 AI 后处理与许可提取
- 配备实时空域/上下文更新的语音 ATC 助手
- 通过 REST + WebSocket 暴露转写历史和运行数据

### 仿真与运行
- 模拟飞行器,可实时控制(航向/速度/垂直速率)
- 仿真流量与真实监控/告警管线的整合
- 设置面板包含 ADS-B 数据源健康状况和精简的解码器指标

## API 文档

Co-ATC 提供了用于访问飞行器数据、频率信息和转写内容的全面 RESTful API。详细的 API 文档(包括端点、请求参数和响应格式)请参阅 [API 规范](docs/api_spec.md)。

## 技术文档

关于系统架构、实现细节和内部工作原理的详细技术信息,请参阅 [技术文档](docs/technical_docs.md)。

## 配置说明

- **数据库**:SQLite 数据库每日创建,文件名形如 `co-atc-YYYY-MM-DD.db`
- **静态文件**:Web 界面文件由可配置目录提供(默认:`www`)
- **音频延迟**:针对低延迟流式传输进行优化,缓冲区大小可配置
- **性能**:支持高频率数据更新,具备智能过滤与缓存
