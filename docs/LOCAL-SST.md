# 提案：使用 Moonshine 实现本地实时 STT

## 概述

将基于云端的 OpenAI 转写（`gpt-4o-transcribe`）替换为使用 **Moonshine** 的本地设备端转写。Moonshine 是一个针对实时边缘推理优化的开源 ASR 模型。

## 动机

- **延迟**：云端转写每次请求会增加约 200-500ms 的网络延迟
- **成本**：OpenAI API 按转写音频的分钟数收费
- **隐私**：当前音频数据需要离开设备
- **可靠性**：依赖于互联网连接和 OpenAI API 可用性

## 当前架构

```
┌─────────────┐     SRT/WAV      ┌─────────────┐    WebSocket    ┌─────────────┐
│ rtl_airband │ ───────────────► │   co-atc    │ ──────────────► │   OpenAI    │
│  (HackRF)   │   16kHz mono     │  Go server  │   Audio chunks  │ gpt-4o-xscr │
└─────────────┘    pcm_s16le     └─────────────┘                 └─────────────┘
                                        │
                                        ▼
                                 ┌─────────────┐
                                 │   SQLite    │
                                 │ transcripts │
                                 └─────────────┘
```

### 当前音频管道（位于 `internal/transcription/`）

1. **SRT Reader** (`internal/audio/srt_reader.go`)：连接到 rtl_airband SRT 流
2. **Central Processor** (`internal/audio/central_processor.go`)：管理音频缓冲
3. **Transcription Manager** (`internal/transcription/manager.go`)：协调转写会话
4. **Processor** (`internal/transcription/processor.go`)：处理 OpenAI WebSocket 连接、VAD 和流传输

### 当前音频格式

来自 rtl_airband 的 SRT 流输出：
- **格式**：`pcm_s16le`（有符号 16 位小端 PCM）
- **采样率**：16000 Hz（16kHz）
- **声道**：1（单声道）
- **比特率**：256 kb/s

这已经是 Moonshine 的最佳格式 —— 无需转码。

## 提议的架构

```
┌─────────────┐     SRT/WAV      ┌─────────────┐     numpy      ┌─────────────┐
│ rtl_airband │ ───────────────► │   co-atc    │ ─────────────► │  Moonshine  │
│  (HackRF)   │   16kHz mono     │  Go server  │   float32[]    │  ONNX/Tiny  │
└─────────────┘    pcm_s16le     └─────────────┘                └─────────────┘
                                        │                              │
                                        │◄─────────────────────────────┘
                                        ▼                         text
                                 ┌─────────────┐
                                 │   SQLite    │
                                 │ transcripts │
                                 └─────────────┘
```

## Moonshine 模型详情

### 模型选项

| 模型 | 参数 | 大小 | 速度 | WER (LibriSpeech) |
|-------|------------|------|-------|-------------------|
| `moonshine/tiny` | 27M | ~190MB | 最快 | 12.66% |
| `moonshine/base` | 62M | ~400MB | 快速 | 10.07% |

**建议**：从 `moonshine/tiny` 开始以获得最低延迟。如果 ATC 术语的准确率不足，则升级至 `base`。

### 输入要求

- **采样率**：16000 Hz（与 SRT 输出匹配 ✓）
- **格式**：Float32 归一化（-1.0 至 1.0）
- **形状**：`[batch, samples]` 或 `[samples]`
- **片段长度**：每次 transcribe 调用 0.1s 至 64s

### 转换（零转码）

```python
import numpy as np

def pcm_s16le_to_float32(pcm_bytes: bytes) -> np.ndarray:
    """Convert raw PCM bytes to Moonshine-compatible float32 array."""
    pcm_int16 = np.frombuffer(pcm_bytes, dtype=np.int16)
    return pcm_int16.astype(np.float32) / 32768.0
```

这是单一的内存操作 —— 无需重采样，无需编解码器转换。

## 实施计划

### 阶段 1：Moonshine Python 服务

创建一个轻量级的 Python 微服务，实现：
1. 通过 Unix socket 或 HTTP 接收音频块
2. 运行 Moonshine ONNX 推理
3. 返回转写文本

```
internal/transcription/
├── processor.go          # Existing OpenAI processor
├── processor_local.go    # NEW: Local Moonshine processor
└── moonshine/
    ├── service.py        # Python inference service
    ├── vad.py            # Voice Activity Detection
    └── requirements.txt
```

#### Python 服务接口

```python
# moonshine/service.py
import moonshine_onnx
import numpy as np
from flask import Flask, request, jsonify

app = Flask(__name__)
model = moonshine_onnx.MoonshineOnnxModel("moonshine/tiny")

@app.route('/transcribe', methods=['POST'])
def transcribe():
    # Receive raw PCM s16le bytes
    pcm_bytes = request.data

    # Convert to float32
    audio = np.frombuffer(pcm_bytes, dtype=np.int16).astype(np.float32) / 32768.0
    audio = audio[np.newaxis, :]  # Add batch dimension

    # Transcribe
    tokens = model.generate(audio)
    text = moonshine_onnx.load_tokenizer().decode_batch(tokens)[0]

    return jsonify({"text": text})

if __name__ == '__main__':
    app.run(host='127.0.0.1', port=5050, threaded=True)
```

### 阶段 2：Go 集成

新增一个调用本地 Moonshine 服务的处理器，替代 OpenAI。

```go
// internal/transcription/processor_local.go

type LocalProcessor struct {
    serviceURL string
    client     *http.Client
    logger     *logger.Logger
}

func (p *LocalProcessor) Transcribe(audioChunk []byte) (string, error) {
    resp, err := p.client.Post(
        p.serviceURL + "/transcribe",
        "application/octet-stream",
        bytes.NewReader(audioChunk),
    )
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var result struct {
        Text string `json:"text"`
    }
    json.NewDecoder(resp.Body).Decode(&result)
    return result.Text, nil
}
```

### 阶段 3：语音活动检测（VAD）

对于流传输，我们需要检测语音边界。可选方案：

1. **Silero VAD**（推荐）：快速、准确，原生 Python
2. **WebRTC VAD**：C 库，非常快但不太准确
3. **基于能量的方法**：基于 RMS 的简单阈值，准确度最低

```python
# moonshine/vad.py
import torch
torch.set_num_threads(1)

model, utils = torch.hub.load(
    repo_or_dir='snakers4/silero-vad',
    model='silero_vad',
    force_reload=False
)

def detect_speech(audio_chunk: np.ndarray, sample_rate: int = 16000) -> bool:
    """Returns True if speech is detected in the audio chunk."""
    tensor = torch.from_numpy(audio_chunk)
    speech_prob = model(tensor, sample_rate).item()
    return speech_prob > 0.5
```

### 阶段 4：流式管道

```
Audio Stream → VAD → Buffer → Moonshine → Post-Process → DB
     │           │       │         │            │
     │           │       │         │            └─ Callsign extraction, cleanup
     │           │       │         └─ ~50-100ms inference
     │           │       └─ Accumulate 2-5s of speech
     │           └─ Detect speech start/end
     └─ 16kHz PCM from SRT
```

#### 分块策略

```python
class StreamingTranscriber:
    def __init__(self):
        self.buffer = []
        self.min_chunk_ms = 500   # Minimum chunk size
        self.max_chunk_ms = 5000  # Maximum chunk size
        self.silence_ms = 300     # Silence to trigger transcription

    def process_audio(self, pcm_chunk: bytes):
        self.buffer.append(pcm_chunk)

        if self.detect_end_of_utterance():
            audio = self.flush_buffer()
            text = self.transcribe(audio)
            return text
        return None
```

## 配置变更

添加到 `configs/config.toml`：

```toml
#######################################################
# Transcription Configuration
#######################################################
[transcription]
# Backend: "openai" or "local"
backend = "local"

# OpenAI settings (used when backend = "openai")
openai_api_key = "sk-..."
model = "gpt-4o-transcribe"

# Local Moonshine settings (used when backend = "local")
[transcription.local]
enabled = true
service_url = "http://127.0.0.1:5050"
model = "moonshine/tiny"  # or "moonshine/base"

# VAD settings
vad_enabled = true
vad_threshold = 0.5
min_speech_ms = 500
max_speech_ms = 10000
silence_trigger_ms = 300

# Prompt for ATC domain (used in post-processing)
prompt = "ATC radio communication. NATO phonetic alphabet. Callsigns, altitudes, headings."
```

## ATC 专用优化

### 自定义词汇增强

Moonshine 通过 tokenizer 提示支持词汇增强。创建一个 ATC 专用提示：

```python
ATC_PROMPT = """
Toronto Tower, Toronto Ground, Toronto Departure, Toronto Arrival.
Callsigns: Air Canada, WestJet, United, Delta, American, Jazz, Porter.
Phonetic: Alpha Bravo Charlie Delta Echo Foxtrot Golf Hotel India Juliet
Kilo Lima Mike November Oscar Papa Quebec Romeo Sierra Tango Uniform
Victor Whiskey X-ray Yankee Zulu.
Clearance, taxi, takeoff, landing, approach, departure, hold short,
runway, altitude, heading, squawk, ident, contact, frequency.
"""
```

### 后处理管道

保留现有的 GPT-4o 后处理，用于：
- 呼号提取与归一化
- 说话者识别（ATC 与飞行员）
- 指令解析

这种混合方法提供了快速的本地转写 + 智能的后处理。

## 性能预期

| 指标 | OpenAI 云端 | Moonshine 本地 |
|--------|--------------|-----------------|
| 延迟（2s 音频） | 300-800ms | 50-150ms |
| 延迟（5s 音频） | 500-1200ms | 100-300ms |
| 成本 | ~$0.006/min | $0 |
| 准确率 (WER) | ~5-8% | ~10-13% |
| 离线可用 | 否 | 是 |

## 依赖项

### Python
```
useful-moonshine-onnx @ git+https://github.com/moonshine-ai/moonshine.git#subdirectory=moonshine-onnx
flask>=2.0
numpy>=1.20
torch>=2.0  # For Silero VAD
```

### 系统
- ONNX Runtime（CPU 或 CUDA）
- tiny 模型约 200MB 磁盘空间，base 模型约 400MB

## 推出计划

1. **第 1 周**：安装 Moonshine，在录制的 ATC 音频上进行基准测试
2. **第 2 周**：实现带 VAD 的 Python 微服务
3. **第 3 周**：与 co-atc Go 代码库集成
4. **第 4 周**：与 OpenAI 进行 A/B 测试，调优参数
5. **第 5 周**：完全切换，监控质量

## 风险与缓解

| 风险 | 缓解措施 |
|------|------------|
| 准确率低于 OpenAI | 保留 OpenAI 作为备用，用于后处理 |
| Jetson CPU 太慢 | 使用 ONNX 优化，仅在静默期间考虑 base |
| ATC 术语识别错误 | 在 ATC 音频语料库上微调（未来工作） |
| 内存压力 | 监控、调整批次大小、使用 tiny 模型 |

## 成功标准

- [ ] 3s 音频片段的转写延迟 < 200ms
- [ ] ATC 通信的词错误率 < 15%
- [ ] 基础转写零云端 API 调用
- [ ] 本地失败时无缝回退到 OpenAI

## 待修改的文件

1. `internal/transcription/manager.go` - 添加后端选择逻辑
2. `internal/transcription/processor.go` - 重构为接口
3. `internal/transcription/processor_local.go` - 新增：本地处理器
4. `internal/config/config.go` - 添加本地转写配置
5. `configs/config.toml` - 添加本地转写设置
6. `moonshine/` - 新增：Python 服务目录

## 参考资料

- [Moonshine GitHub](https://github.com/moonshine-ai/moonshine)
- [Moonshine Paper](https://arxiv.org/abs/2410.15608)
- [Silero VAD](https://github.com/snakers4/silero-vad)
- [ONNX Runtime](https://onnxruntime.ai/)

---

*提案由 Watt 撰写，2026-01-28*
