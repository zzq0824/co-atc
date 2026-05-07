# 通过 faster-whisper 实现本地 STT

## 概述

新增本地语音转文本作为基于云端的 OpenAI Realtime Transcription API（`gpt-4o-transcribe`）的替代方案。使用 **faster-whisper** —— 一个基于 CTranslate2 的 Whisper 实现，比 OpenAI 的开源 whisper 快 4 倍，且准确度相当。

**这取代了之前的 Moonshine 提案。** faster-whisper 提供了显著更好的准确度（Whisper large-v3 级别）、内置的 Silero VAD 以及一个支持 GPU 加速的成熟生态系统。

### 原因

- **成本**：OpenAI API 每分钟音频收费约 $0.006 —— 本地免费
- **延迟**：消除约 200-500ms 的网络往返
- **隐私**：音频永远不离开设备
- **离线**：无需互联网连接即可工作
- **灵活性**：选择适合您硬件的模型大小

---

## 架构

### 方法：Python HTTP 边车

faster-whisper 是 Python/CTranslate2 实现 —— 无法在 Go 中原生运行。一个轻量级的 **FastAPI 服务器**与 co-atc 一起运行，并通过 HTTP 接收 PCM 音频。

```
Audio Stream → CentralAudioProcessor → MultiReader → io.Reader
                                                        │
                                          ┌─────────────┴─────────────┐
                                          │ backend = "openai"         │ backend = "local"
                                          ↓                            ↓
                                    Processor                    LocalProcessor
                                    (WebSocket to OpenAI)        (HTTP to sidecar)
                                          │                            │
                                          ↓                            ↓
                                    ┌─────────────┐             ┌─────────────┐
                                    │  SQLite     │             │  SQLite     │
                                    │  WS Bcast   │             │  WS Bcast   │
                                    │  FileLog    │             │  FileLog    │
                                    └─────────────┘             └─────────────┘
                                          │                            │
                                          └──────────┬─────────────────┘
                                                     ↓
                                          PostProcessor (GPT-4o)
                                          Works with either backend
```

### 关键设计决策

1. **FastAPI 而非 Flask** —— 异步支持、性能更好、自动 OpenAPI 文档
2. **HTTP 而非 gRPC** —— 项目处处使用 HTTP；以每个频率约每 5 秒一次请求的速率，gRPC 增加 protobuf 复杂性带来的好处微不足道
3. **由边车而非 Go 重采样** —— 保持 Go 音频管道不变（24kHz）；边车在内部转换为 16kHz（Whisper 的原生采样率）
4. **VAD 在边车端** —— faster-whisper 内置 Silero VAD；无需 Go 端 VAD 或 torch 依赖
5. **缓冲并刷新** —— Go 累积 N 秒的音频，发送到边车，边车的 VAD 过滤静默

---

## 模型对比

| 模型 | 参数 | 磁盘大小 | VRAM (float16) | VRAM (int8) | 速度 vs 实时 | 准确度 |
|-------|--------|-----------|----------------|-------------|-------------------|----------|
| `tiny` | 39M | 75 MB | ~1 GB | ~0.5 GB | ~30x | 一般 |
| `base` | 74M | 142 MB | ~1 GB | ~0.5 GB | ~20x | 良好 |
| `small` | 244M | 466 MB | ~2 GB | ~1 GB | ~10x | 较好 |
| `medium` | 769M | 1.5 GB | ~4 GB | ~2 GB | ~5x | 优秀 |
| `large-v2` | 1550M | 3.1 GB | ~6 GB | ~3 GB | ~3x | 最佳 |
| `large-v3` | 1550M | 3.1 GB | ~6 GB | ~3 GB | ~3x | 最佳 |

**速度说明**："vs 实时"指在中端 NVIDIA GPU（RTX 3060/4060）上比实时快多少倍。CPU 大约慢 5-10 倍。

### 按硬件推荐

| 硬件 | 推荐模型 | 计算类型 | 备注 |
|----------|------------------|--------------|-------|
| NVIDIA GPU (6GB+ VRAM) | `medium` 或 `large-v3` | `float16` | 最佳准确度，快速推理 |
| NVIDIA GPU (4GB VRAM) | `small` 或 `medium` | `int8` | 良好平衡 |
| NVIDIA GPU (2GB VRAM) | `base` 或 `small` | `int8` | 可用 |
| 仅 CPU（现代 x86） | `small` | `int8` | 可接受的延迟 |
| 仅 CPU（较老/ARM） | `tiny` 或 `base` | `int8` | 最快，准确度较低 |
| Apple Silicon (M1/M2/M3) | `small` | `int8` | CTranslate2 不支持 MPS —— 仅 CPU |

---

## 快速开始

### 1. 运行安装脚本

**Windows:**
```powershell
.\scripts\setup_local_stt_windows.ps1
```

**macOS:**
```bash
./scripts/setup_local_stt_mac.sh
```

**Linux:**
```bash
./scripts/setup_local_stt_linux.sh
```

### 2. 启动 whisper 边车

**Windows:**
```powershell
.\scripts\start_whisper_server.ps1
```

**macOS/Linux:**
```bash
./scripts/start_whisper_server.sh
```

或手动启动：
```bash
cd sidecar
.venv/bin/python whisper_server.py --model medium --device auto --compute-type float16 --port 8178
```

### 3. 配置 co-atc

在 `configs/config.toml` 中设置：
```toml
[transcription]
backend = "local"

[transcription.local]
server_url = "http://localhost:8178"
model_size = "medium"
```

然后正常重新构建并运行 co-atc。

### 4. 下载不同模型进行测试

```bash
python scripts/download_whisper_model.py --model tiny
python scripts/download_whisper_model.py --model base
python scripts/download_whisper_model.py --model small
python scripts/download_whisper_model.py --model medium
python scripts/download_whisper_model.py --model large-v3
```

模型缓存在 `~/.cache/huggingface/hub/` —— 下载一次，永久使用。

---

## Python 边车设计

### 文件结构

```
sidecar/
├── whisper_server.py      # FastAPI server (main entry point)
├── config.py              # Configuration dataclass + CLI arg parsing
└── requirements.txt       # Python dependencies
```

### API 端点

#### `POST /transcribe`

接收原始 PCM 音频，返回转写。

**请求:**
- Body: 原始 PCM 字节（s16le 格式）
- Headers:
  - `Content-Type: application/octet-stream`
  - `X-Sample-Rate: 24000`（或 16000 —— 边车在内部重采样）
  - `X-Channels: 1`

**响应:**
```json
{
  "text": "Air Canada 123 contact Toronto Tower 118.7",
  "segments": [
    {
      "text": " Air Canada 123 contact Toronto Tower 118.7",
      "start": 0.0,
      "end": 3.2,
      "no_speech_prob": 0.05
    }
  ],
  "language": "en",
  "duration": 5.0
}
```

#### `GET /health`

**响应:**
```json
{
  "status": "ok",
  "model": "medium",
  "device": "cuda",
  "compute_type": "float16"
}
```

### 关键实现细节

```python
from faster_whisper import WhisperModel
from fastapi import FastAPI, Request, Header
import numpy as np
import threading

app = FastAPI()
model: WhisperModel = None
model_lock = threading.Lock()  # WhisperModel.transcribe() is NOT thread-safe

@app.on_event("startup")
def load_model():
    global model
    model = WhisperModel(
        config.model_size,
        device=config.device,
        compute_type=config.compute_type
    )

@app.post("/transcribe")
async def transcribe(request: Request, x_sample_rate: int = Header(24000)):
    pcm_bytes = await request.body()
    audio = np.frombuffer(pcm_bytes, dtype=np.int16).astype(np.float32) / 32768.0

    # Resample to 16kHz if needed (Whisper's native rate)
    if x_sample_rate != 16000:
        from scipy.signal import resample
        target_len = int(len(audio) * 16000 / x_sample_rate)
        audio = resample(audio, target_len)

    with model_lock:
        segments, info = model.transcribe(
            audio,
            language=config.language,
            beam_size=config.beam_size,
            vad_filter=config.vad_filter,
            vad_parameters=dict(
                threshold=config.vad_threshold,
                min_silence_duration_ms=config.min_silence_duration_ms,
            ),
        )
        result = list(segments)  # Consume the generator inside the lock

    return {
        "segments": [{"text": s.text, "start": s.start, "end": s.end, "no_speech_prob": s.no_speech_prob} for s in result],
        "text": " ".join(s.text for s in result).strip(),
        "language": info.language,
        "duration": info.duration,
    }
```

**依赖项**（`sidecar/requirements.txt`）：
```
faster-whisper>=1.1.0
fastapi>=0.104.0
uvicorn[standard]>=0.24.0
numpy>=1.24.0
scipy>=1.11.0
```

注意：faster-whisper 捆绑了 CTranslate2 和 Silero VAD —— 无需单独安装 torch。

---

## Go 集成设计

### 新的配置字段

**`internal/config/config.go`** —— 添加到 `TranscriptionConfig`：
```go
Backend string             `toml:"backend"` // "openai" (default) or "local"
Local   LocalWhisperConfig `toml:"local"`
```

新结构体：
```go
type LocalWhisperConfig struct {
    ServerURL            string  `toml:"server_url"`             // Sidecar URL (default: http://localhost:8178)
    ModelSize            string  `toml:"model_size"`             // tiny, base, small, medium, large-v2, large-v3
    Device               string  `toml:"device"`                 // auto, cpu, cuda
    ComputeType          string  `toml:"compute_type"`           // float16, int8, int8_float16, float32
    Language             string  `toml:"language"`               // Language code (default: en)
    BeamSize             int     `toml:"beam_size"`              // Beam search width (default: 5)
    VADFilter            bool    `toml:"vad_filter"`             // Enable Silero VAD (default: true)
    VADThreshold         float64 `toml:"vad_threshold"`          // VAD sensitivity (default: 0.5)
    MinSilenceDurationMs int     `toml:"min_silence_duration_ms"` // Silence to split speech (default: 500)
    BufferSeconds        int     `toml:"buffer_seconds"`         // Audio accumulation window (default: 5)
    MaxBufferSeconds     int     `toml:"max_buffer_seconds"`     // Force flush threshold (default: 30)
    TimeoutSeconds       int     `toml:"timeout_seconds"`        // HTTP timeout for sidecar (default: 30)
}
```

在 `internal/transcription/models.go`（`transcription.Config` 结构体）中**镜像**这些字段。

### TOML 配置示例

```toml
[transcription]
# ... existing OpenAI fields ...

# Backend: "openai" (cloud, default) or "local" (faster-whisper sidecar)
backend = "openai"

# Local faster-whisper settings (used when backend = "local")
# Requires the whisper sidecar server running. See docs/LOCAL-STT.md
[transcription.local]
server_url = "http://localhost:8178"
model_size = "medium"              # tiny, base, small, medium, large-v2, large-v3
device = "auto"                    # auto, cpu, cuda
compute_type = "float16"           # float16, int8, int8_float16, float32
language = "en"
beam_size = 5
vad_filter = true
vad_threshold = 0.5
min_silence_duration_ms = 500
buffer_seconds = 5                 # Accumulate N seconds before sending to sidecar
max_buffer_seconds = 30            # Force flush if buffer exceeds this
timeout_seconds = 30               # HTTP timeout for sidecar requests
```

### LocalProcessor (`internal/transcription/local_processor.go`)

实现 `ProcessorInterface`（与 OpenAI 的 `Processor` 相同）。

```go
type LocalProcessor struct {
    frequencyID    string
    audioReader    io.ReadCloser
    sidecarURL     string
    httpClient     *http.Client
    wsServer       *websocket.Server
    storage        *sqlite.TranscriptionStorage
    ctx            context.Context
    cancel         context.CancelFunc
    logger         *logger.Logger
    config         Config
    fileLogger     *FileLogger
    // Audio accumulation
    audioBuffer    []byte
    audioBufferMu  sync.Mutex
}
```

**音频策略 —— 固定窗口加服务端 VAD：**

1. `processAudio()` 协程持续从 `audioReader` 读取，追加到 `audioBuffer`
2. `transcriptionLoop()` 协程每 `buffer_seconds` 运行一次：
   - 拍下缓冲区快照并清空
   - 通过 HTTP POST 将原始 PCM 发送到 `sidecar_url/transcribe`
   - 边车运行 Silero VAD + faster-whisper
   - 如果未检测到语音 → 空响应 → 静默丢弃
   - 如果检测到语音 → 存入 SQLite，通过 WebSocket 广播，写入 FileLogger
3. `max_buffer_seconds` 防止边车响应慢/宕机时无限制累积

**共享逻辑提取：**

应将 `Processor.processTranscriptionEvent()` 中的 DB 存储 + WebSocket 广播 + FileLogger 逻辑提取到共享辅助函数中，使两种处理器使用相同的代码路径。这确保以下行为一致：
- SQLite `StoreTranscription()` 调用
- WebSocket `transcription` 事件广播
- 文件日志（原始转写日志）

### Manager 分支（`internal/transcription/manager.go`）

`NewProcessor()` 被调用的两个插入点（第 155 行和第 236 行）：

```go
var processor ProcessorInterface
var err error
if m.transcriptionConfig.Backend == "local" {
    processor, err = NewLocalProcessor(ctx, frequencyID, reader, m.transcriptionConfig, m.wsServer, m.transcriptionStorage, m.logger, m.fileLogger)
} else {
    processor, err = NewProcessor(ctx, frequencyID, reader, m.transcriptionConfig, m.wsServer, m.transcriptionStorage, m.logger, m.fileLogger)
}
```

**API key 守卫更新**（manager.go:197）：检查 `if m.openAIAPIKey == ""` 必须更新为也允许 `backend == "local"` 在没有 API key 的情况下运行。

### 配置映射（`internal/frequencies/service.go`）

在第 511 行附近构造 `transcription.Config` 的位置映射 `Backend` 和 `Local` 字段。在第 198 行附近为本地后端更新跳过守卫。

---

## 安装脚本设计

### `scripts/setup_local_stt_windows.ps1`

```powershell
# 1. Check Python 3.10+ is installed
# 2. Create venv at sidecar/.venv
# 3. Activate venv, pip install -r sidecar/requirements.txt
# 4. Detect NVIDIA GPU via nvidia-smi
# 5. If CUDA available: pip install ctranslate2 with CUDA wheels
# 6. Download default model: python -c "from faster_whisper import WhisperModel; WhisperModel('medium')"
# 7. Print success + start instructions
```

### `scripts/setup_local_stt_linux.sh`

相同逻辑，bash 实现。附加：通过 `nvidia-smi` 检测 CUDA，如有需要处理 apt 依赖（`python3-venv`）。

### `scripts/setup_local_stt_mac.sh`

相同逻辑，bash 实现。**重要说明：**
- CTranslate2 不支持 MPS（Apple Silicon GPU）—— 仅 CPU
- 推荐使用 `compute_type = "int8"` 以获得 CPU 性能
- 推荐使用 `model_size = "small"` 以获得合理的 CPU 延迟

### `scripts/start_whisper_server.ps1` / `scripts/start_whisper_server.sh`

激活 venv，使用配置的参数启动 `sidecar/whisper_server.py`。透传 CLI 参数。

### `scripts/download_whisper_model.py`

下载特定模型到 HuggingFace 缓存：
```
Usage: python scripts/download_whisper_model.py --model <size>
Sizes: tiny, base, small, medium, large-v2, large-v3
```

---

## 性能预期

| 指标 | OpenAI 云端 | 本地（GPU，medium） | 本地（CPU，small） |
|--------|--------------|--------------------|--------------------|
| 延迟（5s 音频） | 500-1200ms | 200-500ms | 1-3s |
| 成本 | ~$0.006/min | $0 | $0 |
| 准确率 (WER) | ~5-8% | ~7-10% | ~10-15% |
| 离线可用 | 否 | 是 | 是 |
| 需要 GPU | 否 | 推荐 | 否 |

**延迟说明**：缓冲窗口增加了固定延迟（默认 5s）。总延迟 = buffer_seconds + 推理时间。对于 ATC 监控这是可接受的。将 `buffer_seconds` 减少到 3 可获得更快响应，但会增加边车调用次数。

---

## 错误处理

| 场景 | 行为 |
|----------|----------|
| 启动时边车未运行 | `LocalProcessor.Start()` 健康检查边车；返回错误，跳过该频率 |
| 边车在运行中崩溃 | 记录错误，继续累积音频，下次刷新周期重试 |
| 空转写（静默） | 边车返回 `{"text": ""}`，静默丢弃 |
| 缓冲过长（边车响应慢） | `max_buffer_seconds` 强制刷新，旧音频丢弃并发出警告 |
| 配置 `backend` 为空/缺失 | 默认为 `"openai"` —— 完全向后兼容 |

---

## 实施阶段

### 阶段 1：Python 边车
创建 `sidecar/` 目录，包含 `whisper_server.py`、`config.py`、`requirements.txt`。使用 curl 进行独立测试。

### 阶段 2：Go 配置
将 `Backend`、`LocalWhisperConfig` 添加到配置结构体。更新 `config.toml.example`。通过 frequencies 服务进行映射。

### 阶段 3：Go LocalProcessor
提取共享转写事件处理器。创建 `local_processor.go`。在 manager 中添加后端分支。

### 阶段 4：安装脚本与文档
为所有 3 个平台编写安装脚本 + 模型下载脚本 + 启动脚本。用最终细节更新本文档。

### 阶段 5：集成测试
端到端：SRT/HTTP 流 → Go → 边车 → SQLite → WebSocket → UI。验证后处理仍能正常工作。测试后端切换。

---

## 文件汇总

### 待创建
| 文件 | 用途 |
|------|---------|
| `sidecar/whisper_server.py` | FastAPI 转写服务器 |
| `sidecar/config.py` | 服务器配置 |
| `sidecar/requirements.txt` | Python 依赖项 |
| `internal/transcription/local_processor.go` | Go 本地处理器 |
| `scripts/setup_local_stt_windows.ps1` | Windows 安装 |
| `scripts/setup_local_stt_linux.sh` | Linux 安装 |
| `scripts/setup_local_stt_mac.sh` | macOS 安装 |
| `scripts/start_whisper_server.ps1` | Windows 启动脚本 |
| `scripts/start_whisper_server.sh` | Unix 启动脚本 |
| `scripts/download_whisper_model.py` | 模型下载器 |

### 待修改
| 文件 | 变更 |
|------|--------|
| `internal/config/config.go` | 添加 `Backend`、`LocalWhisperConfig` |
| `internal/transcription/models.go` | 镜像配置字段 |
| `internal/transcription/interface.go` | 添加 `LocalProcessor` 编译时检查 |
| `internal/transcription/processor.go` | 提取共享存储/广播辅助函数 |
| `internal/transcription/manager.go` | 后端分支逻辑 |
| `internal/frequencies/service.go` | 配置映射 + API key 守卫 |
| `configs/config.toml.example` | 新的 `[transcription.local]` 章节 |

---

## 参考资料

- [faster-whisper](https://github.com/SYSTRAN/faster-whisper) —— 基于 CTranslate2 的 Whisper 实现
- [CTranslate2](https://github.com/OpenNMT/CTranslate2) —— Transformer 模型的快速推理引擎
- [Silero VAD](https://github.com/snakers4/silero-vad) —— 语音活动检测（已捆绑在 faster-whisper 中）
- [FastAPI](https://fastapi.tiangolo.com/) —— Python 异步 Web 框架
- [OpenAI Whisper](https://github.com/openai/whisper) —— 原始模型架构

---

## 故障排查

### 边车无法启动
- 确保安装了 Python 3.10+：`python --version`
- 确保 venv 已激活：`sidecar/.venv/Scripts/activate`（Windows）或 `source sidecar/.venv/bin/activate`
- 检查端口 8178 是否可用：`netstat -an | findstr 8178`

### 未检测到 CUDA
- 验证 NVIDIA 驱动：`nvidia-smi`
- 验证 CUDA 工具包：`nvcc --version`
- faster-whisper 需要带 CUDA 的 CTranslate2 —— 重新安装：`pip install ctranslate2 --force-reinstall`
- 回退方案：设置 `device = "cpu"` 和 `compute_type = "int8"`

### 转写缓慢
- 使用更小的模型（`small` 而非 `medium`）
- 使用 `int8` 计算类型（CPU 上快 2 倍，准确度略降）
- 将 `beam_size` 从 5 减到 1（更快，准确度略降）
- 如有可用 GPU 请确保使用（`device = "auto"`）

### 空转写
- 检查 `vad_threshold` —— 如果遗漏了语音，请降低（例如 0.3）
- 检查音频是否到达边车 —— 查看 Go 日志中的 HTTP POST 活动
- 直接测试边车：`curl -X POST http://localhost:8178/transcribe -H "Content-Type: application/octet-stream" --data-binary @test.pcm`

### 模型下载问题
- 模型从 HuggingFace 下载 —— 安装期间确保有互联网连接
- 缓存位置：`~/.cache/huggingface/hub/`
- 手动下载：`python -c "from faster_whisper import WhisperModel; WhisperModel('medium')"`
