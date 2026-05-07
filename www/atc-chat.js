// ATC Chat 前端组件
class ATCChat {
    constructor() {
        this.sessionId = null;
        this.isRecording = false;
        this.isConnected = false;
        this.websocket = null;
        this.mediaRecorder = null;
        this.audioContext = null;
        this.stream = null;
        this.audioQueue = [];
        this.audioBuffer = [];
        this.scriptProcessor = null;
        this.isPlaying = false;
        this.pushToTalkActive = false;
        this.currentAIResponse = null; // 用于累积 AI 响应文本
        this.aiVisualizationFrameId = null;
        this.audioAnalyser = null;
        this.audioDataArray = null;

        // 转写功能
        this.transcripts = [];
        this.filteredTranscripts = [];
        this.transcriptViewerVisible = false;
        this.transcriptSearchTerm = '';
        this.transcriptIdCounter = 0;

        // 初始化已过滤的转写
        this.filterTranscripts();

        this.init();
    }

    // 视觉状态指示器方法
    showStatusIndicator(state, text, showAnimation = false) {
        const statusElement = document.getElementById('ai-advisory-status');
        const activityIndicator = document.getElementById('ai-activity-indicator');
        const visBar = document.getElementById('ai-vis-bar');

        if (!statusElement) return;

        // 更新状态文本
        statusElement.textContent = text;

        // 更新活动指示器
        if (activityIndicator) {
            switch (state) {
                case 'transmitting':
                case 'push-to-talk':
                    activityIndicator.textContent = 'TX';
                    activityIndicator.className = 'absolute top-0 right-1 text-xs text-red-400 p-0.5 font-bold animate-pulse';
                    break;
                case 'processing':
                    activityIndicator.textContent = 'AI';
                    activityIndicator.className = 'absolute top-0 right-1 text-xs text-yellow-400 p-0.5 font-bold animate-pulse';
                    break;
                case 'playing':
                    activityIndicator.textContent = 'RX';
                    activityIndicator.className = 'absolute top-0 right-1 text-xs text-green-400 p-0.5 font-bold animate-pulse';
                    break;
                case 'connected':
                    activityIndicator.textContent = 'RDY';
                    activityIndicator.className = 'absolute top-0 right-1 text-xs text-purple-400 p-0.5 font-bold';
                    break;
                case 'disconnecting':
                    activityIndicator.textContent = 'END';
                    activityIndicator.className = 'absolute top-0 right-1 text-xs text-orange-400 p-0.5 font-bold animate-pulse';
                    break;
                default:
                    activityIndicator.textContent = '--';
                    activityIndicator.className = 'absolute top-0 right-1 text-xs text-neutral-400 p-0.5';
            }
            activityIndicator.style.fontSize = '0.6rem';
            activityIndicator.style.lineHeight = '1';
            activityIndicator.style.pointerEvents = 'none';
        }
    }

    hideStatusIndicator() {
        const statusElement = document.getElementById('ai-advisory-status');
        const activityIndicator = document.getElementById('ai-activity-indicator');

        if (statusElement) {
            statusElement.textContent = '已断开';
        }
        if (activityIndicator) {
            activityIndicator.textContent = '--';
            activityIndicator.className = 'absolute top-0 right-1 text-xs text-neutral-400 p-0.5';
            activityIndicator.style.fontSize = '0.6rem';
            activityIndicator.style.lineHeight = '1';
            activityIndicator.style.pointerEvents = 'none';
        }
    }

    // PTT 视觉反馈方法
    addPTTVisualFeedback() {
        const aiContainer = document.getElementById('ai-advisory-container');
        if (aiContainer) {
            aiContainer.classList.add('ptt-active');
        }
    }

    removePTTVisualFeedback() {
        const aiContainer = document.getElementById('ai-advisory-container');
        if (aiContainer) {
            aiContainer.classList.remove('ptt-active');
        }
    }

    async init() {
        console.log('[ATC-Chat] 正在初始化 ATC Chat...');

        // 检查是否启用了 ATC Chat
        try {
            const response = await fetch(`/api/v1/config`);
            if (!response.ok) {
                console.log('[ATC-Chat] 配置不可用，状态:', response.status);
                // 即使配置失败也尝试创建按钮
                this.createChatButton();
                this.setupKeyboardListeners();
                return;
            }

            const config = await response.json();
            console.log('[ATC-Chat] 配置已加载:', config);

            if (!config.atc_chat?.enabled) {
                console.log('[ATC-Chat] 配置中已禁用 ATC Chat');
                // 如果禁用则隐藏按钮
                const chatBtn = document.getElementById('atc-chat-btn');
                if (chatBtn) {
                    chatBtn.style.display = 'none';
                }
                return;
            }

            console.log('[ATC-Chat] ATC Chat 已启用，正在设置...');
            this.createChatButton();
            this.setupKeyboardListeners();
        } catch (error) {
            console.error('[ATC-Chat] ATC Chat 初始化错误:', error);
            // 即使有错误也尝试创建按钮
            this.createChatButton();
            this.setupKeyboardListeners();
        }
    }

    createChatButton() {
        console.log('[ATC-Chat] 正在设置 AI Advisory 界面...');

        // 使 ATC Chat 实例对 UI 全局可用
        window.atcChat = this;

        // 为动态状态添加 CSS 样式
        this.addStyles();

        console.log('[ATC-Chat] AI Advisory 界面设置完成');
    }

    setupKeyboardListeners() {
        let spacePressed = false;

        document.addEventListener('keydown', (event) => {
            const isInInputField = event.target.tagName === 'INPUT' ||
                event.target.tagName === 'TEXTAREA' ||
                event.target.tagName === 'SELECT' ||
                event.target.isContentEditable;

            if (isInInputField) {
                return;
            }

            if (event.code === 'Space' && !event.repeat && this.isConnected && !spacePressed) {
                event.preventDefault();
                spacePressed = true;
                this.addPTTVisualFeedback();
                this.startPushToTalk();
            }
        });

        document.addEventListener('keyup', (event) => {
            const isInInputField = event.target.tagName === 'INPUT' ||
                event.target.tagName === 'TEXTAREA' ||
                event.target.tagName === 'SELECT' ||
                event.target.isContentEditable;

            if (isInInputField) {
                return;
            }

            if (event.code === 'Space' && spacePressed) {
                event.preventDefault();
                spacePressed = false;
                this.removePTTVisualFeedback();
                this.stopPushToTalk();
            }
        });
    }

    addStyles() {
        const style = document.createElement('style');
        style.textContent = `
            .atc-chat-button.recording {
                background: linear-gradient(135deg, #ff6b6b 0%, #ee5a24 100%) !important;
                animation: pulse 1s infinite;
            }

            .atc-chat-button.connected {
                background: linear-gradient(135deg, #00d2d3 0%, #54a0ff 100%) !important;
            }

            .atc-chat-button.disabled {
                background: #6c757d !important;
                cursor: not-allowed;
                opacity: 0.6;
            }

            .atc-chat-button.push-to-talk {
                background: linear-gradient(135deg, #ff9f43 0%, #f0932b 100%) !important;
                animation: glow 1.5s infinite alternate;
            }

            .atc-chat-button.connecting {
                background: linear-gradient(135deg, #ffa726 0%, #ff7043 100%) !important;
                animation: pulse 1.5s infinite;
            }

            @keyframes pulse {
                0% { transform: scale(1); }
                50% { transform: scale(1.05); }
                100% { transform: scale(1); }
            }

            @keyframes glow {
                0% { box-shadow: 0 2px 8px rgba(255, 159, 67, 0.4); }
                100% { box-shadow: 0 4px 16px rgba(255, 159, 67, 0.8); }
            }

            #atc-chat-status.active {
                display: block !important;
                animation: blink 2s infinite;
            }

            @keyframes blink {
                0%, 50% { opacity: 1; }
                51%, 100% { opacity: 0.3; }
            }

            #ai-advisory-container.ptt-active {
                border-color: #ef4444 !important;
                box-shadow: 0 0 0 2px rgba(239, 68, 68, 0.3) !important;
            }
        `;
        document.head.appendChild(style);
    }

    async toggleChat() {
        if (!this.isConnected) {
            await this.startChat();
        } else {
            // 移除确认对话框 - 直接断开连接
            await this.endChat();
        }
    }

    async startChat() {
        try {
            console.log('[ATC-Chat] 正在启动 ATC Chat...');
            this.showStatusIndicator('connected', '正在连接...');

            // 创建会话
            console.log('[ATC-Chat] 正在创建会话...');
            const response = await fetch(`/api/v1/atc-chat/session`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                }
            });

            console.log('[ATC-Chat] 会话响应状态:', response.status);
            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(`创建会话失败: ${response.status} ${response.statusText} - ${errorText}`);
            }

            const session = await response.json();
            this.sessionId = session.id;

            console.log('[ATC-Chat] ATC Chat 会话已创建:', this.sessionId);

            // 初始化音频
            console.log('[ATC-Chat] 正在初始化音频...');
            await this.initializeAudio();

            // 连接 WebSocket
            console.log('[ATC-Chat] 正在连接 WebSocket...');
            await this.connectWebSocket();

            this.isConnected = true;
            this.showStatusIndicator('connected', 'PTT - 按住空格键');

            // 启动 AI 音频可视化
            this.startAIAudioVisualization();

            // 触发 Alpine.js 响应式
            this.triggerReactivity();

            console.log('[ATC-Chat] ATC Chat 启动成功');

        } catch (error) {
            console.error('[ATC-Chat] 启动 ATC Chat 失败:', error);
            this.hideStatusIndicator();
            setTimeout(() => {
                this.hideStatusIndicator();
            }, 3000);
        }
    }

    async endChat() {
        try {
            this.showStatusIndicator('disconnecting', '正在结束...');

            // 如果正在录音则停止
            if (this.isRecording) {
                this.stopRecording();
            }

            // 停止 AI 音频可视化
            this.stopAIAudioVisualization();

            // 首先用适当的关闭代码关闭 WebSocket
            // 这将自动触发服务器端清理
            if (this.websocket) {
                this.websocket.close(1000, 'Session ended by user');
                this.websocket = null;
            }

            // 停止音频流
            if (this.stream) {
                this.stream.getTracks().forEach(track => track.stop());
                this.stream = null;
            }

            // 关闭音频上下文
            if (this.audioContext) {
                await this.audioContext.close();
                this.audioContext = null;
            }

            // 清理音频分析
            this.audioAnalyser = null;
            this.audioDataArray = null;

            // 不要调用 DELETE 端点 - WebSocket 关闭将自动触发服务器端清理
            // 这可以防止重复的会话终止错误
            if (this.sessionId) {
                console.log('[ATC-Chat] 会话清理将由 WebSocket 关闭来处理');
                this.sessionId = null;
            }

            this.isConnected = false;

            // 隐藏状态指示器
            const statusIndicator = document.getElementById('atc-chat-status');
            if (statusIndicator) {
                statusIndicator.classList.remove('active');
            }

            // 重置状态为已断开
            this.hideStatusIndicator();

            // 触发 Alpine.js 响应式
            this.triggerReactivity();

            console.log('[ATC-Chat] ATC Chat 会话已结束');

        } catch (error) {
            console.error('[ATC-Chat] 结束 ATC Chat 失败:', error);
            this.hideStatusIndicator();
        }
    }

    async initializeAudio() {
        try {
            // 请求麦克风访问权限
            this.stream = await navigator.mediaDevices.getUserMedia({
                audio: {
                    sampleRate: 24000,
                    channelCount: 1,
                    echoCancellation: true,
                    noiseSuppression: true,
                    autoGainControl: true
                }
            });

            // 创建音频上下文
            this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
                sampleRate: 24000
            });

            // 设置用于可视化的音频分析器
            try {
                const sourceNode = this.audioContext.createMediaStreamSource(this.stream);
                this.audioAnalyser = this.audioContext.createAnalyser();
                this.audioAnalyser.fftSize = 256;
                this.audioAnalyser.smoothingTimeConstant = 0.5;
                this.audioDataArray = new Uint8Array(this.audioAnalyser.frequencyBinCount);

                sourceNode.connect(this.audioAnalyser);
                // 不要连接到目标设备以避免反馈

                console.log('[ATC-Chat] 已设置用于可视化的音频分析器');
            } catch (e) {
                console.warn('[ATC-Chat] 无法设置音频分析器:', e);
                // 在没有分析器的情况下继续 - 可视化将使用回退方案
            }

            console.log('[ATC-Chat] 音频初始化成功');

        } catch (error) {
            throw new Error(`初始化音频失败: ${error.message}`);
        }
    }

    async connectWebSocket() {
        return new Promise((resolve, reject) => {
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const wsUrl = `${protocol}//${window.location.host}/api/v1/atc-chat/ws/${this.sessionId}`;

            // 在创建 WebSocket 之前先设置超时
            const timeout = setTimeout(() => {
                if (this.websocket && this.websocket.readyState !== WebSocket.OPEN) {
                    console.log('[ATC-Chat] WebSocket 连接超时 - 正在关闭连接');
                    this.websocket.close();
                    reject(new Error('WebSocket 连接超时'));
                }
            }, 15000); // 超时增加到 15 秒

            this.websocket = new WebSocket(wsUrl);

            this.websocket.onopen = () => {
                clearTimeout(timeout);
                console.log('[ATC-Chat] ATC Chat WebSocket 已连接');
                this.isConnected = true;
                resolve();
            };

            this.websocket.onmessage = (event) => {
                this.handleWebSocketMessage(event);
            };

            this.websocket.onerror = (error) => {
                clearTimeout(timeout);
                console.error('[ATC-Chat] WebSocket 错误:', error);
                reject(error);
            };

            this.websocket.onclose = () => {
                clearTimeout(timeout);
                console.log('[ATC-Chat] ATC Chat WebSocket 已断开');
                if (this.isConnected) {
                    this.endChat();
                }
            };
        });
    }

    handleWebSocketMessage(event) {
        try {
            // 处理文本和二进制消息
            if (event.data instanceof Blob) {
                // 来自 OpenAI 的二进制音频数据
                this.handleBinaryAudio(event.data);
                return;
            }

            // 文本消息
            const message = JSON.parse(event.data);
            //console.log('收到 WebSocket 消息:', message);

            switch (message.type) {
                case 'connection_ready':
                    console.log('[ATC-Chat] 服务器连接已建立，等待 OpenAI...');
                    break;
                case 'openai_ready':
                    console.log('[ATC-Chat] OpenAI 连接已建立，可以进行语音聊天！');
                    break;
                case 'connection_error':
                    console.error('[ATC-Chat] 连接错误:', message.error);
                    break;
                case 'session.update':
                    // 用完整负载记录会话更新事件
                    console.log('[ATC-Chat] 收到会话更新:', message);
                    break;
                case 'response.audio.delta':
                    // OpenAI 实时 API 音频响应
                    if (message.delta) {
                        console.log('[ATC-Chat] 收到音频增量，长度:', message.delta.length);
                        this.queueAudioData(message.delta);
                    }
                    break;
                case 'response.audio.done':
                    // 音频响应完成
                    console.log('[ATC-Chat] 音频响应完成，队列长度:', this.audioQueue.length);
                    this.playQueuedAudio();
                    break;
                case 'response.text.delta':
                    // 累积 AI 响应文本以便记录
                    if (!this.currentAIResponse) {
                        this.currentAIResponse = '';
                    }
                    if (message.delta) {
                        this.currentAIResponse += message.delta;
                    }
                    break;
                case 'response.text.done':
                    // 记录完整的 AI 响应
                    if (this.currentAIResponse) {
                        console.log('[ATC-Chat] Chat - AI-ATC:', this.currentAIResponse);
                        this.addTranscript('AI', this.currentAIResponse);
                        this.currentAIResponse = null;
                    }
                    break;
                case 'response.audio_transcript.delta':
                    // 累积 AI 音频转写以便记录（替代文本来源）
                    if (!this.currentAITranscript) {
                        this.currentAITranscript = '';
                    }
                    if (message.delta) {
                        this.currentAITranscript += message.delta;
                    }
                    break;
                case 'response.audio_transcript.done':
                    // 记录完整的 AI 音频转写
                    if (this.currentAITranscript) {
                        console.log('[ATC-Chat] Chat - AI-ATC:', this.currentAITranscript);
                        // 仅在我们尚未拥有文本响应时添加
                        if (!this.currentAIResponse) {
                            this.addTranscript('AI', this.currentAITranscript);
                        }
                        this.currentAITranscript = null;
                    }
                    break;
                case 'conversation.item.input_audio_transcription.completed':
                    // 记录用户的转写语音
                    if (message.transcript) {
                        console.log('[ATC-Chat] Chat - Pilot:', message.transcript);
                        this.addTranscript('PILOT', message.transcript);
                    }
                    break;
                case 'conversation.item.created':
                    console.log('[ATC-Chat] AI 响应已开始');
                    this.showStatusIndicator('processing', 'AI 正在响应...');

                    // 清除任何现有的音频队列以防止旧音频重新播放
                    if (this.audioQueue.length > 0) {
                        console.log('[ATC-Chat] 正在清除带有', this.audioQueue.length, '项的旧音频队列');
                        this.audioQueue = [];
                    }

                    // 记录完整消息以查看可用的数据
                    if (message.item && message.item.content) {
                        console.log('[ATC-Chat] Chat - AI-ATC:', message.item.content);
                    }
                    break;
                case 'response.done':
                    // 响应完全结束 - 返回到就绪状态
                    console.log('[ATC-Chat] AI 响应已完成');
                    this.showStatusIndicator('connected', 'PTT - 按住空格键');
                    if (message.response && message.response.output) {
                        console.log('[ATC-Chat] Chat - AI-ATC:', message.response.output);
                    }
                    break;
                case 'error':
                    console.error('[ATC-Chat] ATC Chat 错误:', message.error);
                    break;
                default:
                    //console.log('[ATC-Chat] OpenAI 消息类型:', message.type, message);
            }
        } catch (error) {
            console.error('[ATC-Chat] 解析 WebSocket 消息失败:', error);
        }
    }

    handleBinaryAudio(blob) {
        // 处理二进制音频数据
        this.queueAudioBlob(blob);
    }

    async startPushToTalk() {
        if (!this.isConnected || this.pushToTalkActive) return;

        this.pushToTalkActive = true;

        // 立即开始监听以避免在上下文刷新进行中丢失语音。
        this.showStatusIndicator('push-to-talk', '正在监听...');
        this.startRecording();

        // 异步刷新上下文；不要阻塞 PTT 录音。
        this.updateSessionContext().catch((error) => {
            console.error('[ATC-Chat] PTT 期间异步上下文更新失败:', error);
        });
    }

    async stopPushToTalk() {
        if (!this.pushToTalkActive) return;

        this.pushToTalkActive = false;
        this.showStatusIndicator('processing', '正在处理...');
        this.stopRecording();

        // 当 AI 响应完成时将返回到就绪状态
    }

    async updateSessionContext() {
        if (!this.sessionId) {
            console.warn('[ATC-Chat] 没有可用于上下文更新的会话 ID');
            return;
        }

        try {
            const response = await fetch(`/api/v1/atc-chat/session/${this.sessionId}/update-context`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json'
                }
            });

            if (!response.ok) {
                throw new Error(`HTTP ${response.status}: ${response.statusText}`);
            }

            console.log('[ATC-Chat] 会话上下文更新成功');
        } catch (error) {
            console.error('[ATC-Chat] 更新会话上下文失败:', error);
            // 不要抛出 - 即使上下文更新失败也继续 PTT
        }
    }

    startRecording() {
        if (!this.stream || this.isRecording) return;

        try {
            // 创建用于 PCM 转换的音频上下文
            if (!this.audioContext) {
                this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
                    sampleRate: 24000
                });
            }

            // 创建媒体流源
            const source = this.audioContext.createMediaStreamSource(this.stream);

            // 为 PCM 数据创建脚本处理器
            this.scriptProcessor = this.audioContext.createScriptProcessor(4096, 1, 1);
            this.audioBuffer = [];

            this.scriptProcessor.onaudioprocess = (event) => {
                if (this.isRecording) {
                    const inputBuffer = event.inputBuffer;
                    const inputData = inputBuffer.getChannelData(0); // Float32Array

                    // 直接存储 Float32Array 数据
                    // 我们将在发送时转换为 PCM16
                    const audioChunk = new Float32Array(inputData.length);
                    audioChunk.set(inputData);
                    this.audioBuffer.push(audioChunk);
                }
            };

            // 连接音频图
            source.connect(this.scriptProcessor);
            this.scriptProcessor.connect(this.audioContext.destination);

            this.isRecording = true;
            console.log('[ATC-Chat] 已开始使用 Float32 捕获录音');

        } catch (error) {
            console.error('[ATC-Chat] 开始录音失败:', error);
        }
    }

    stopRecording() {
        if (!this.isRecording) return;

        this.isRecording = false;

        // 断开音频处理
        if (this.scriptProcessor) {
            this.scriptProcessor.disconnect();
            this.scriptProcessor = null;
        }

        // 转换并发送累积的 PCM 数据
        this.convertAndSendPCMAudio();

        console.log('[ATC-Chat] 已停止录音');
    }

    convertAndSendPCMAudio() {
        try {
            if (this.audioBuffer.length === 0) {
                console.warn('[ATC-Chat] 没有要发送的音频数据');
                return;
            }

            // 合并所有 Float32Array 块
            let totalLength = 0;
            for (const chunk of this.audioBuffer) {
                totalLength += chunk.length;
            }

            const combinedFloat32 = new Float32Array(totalLength);
            let offset = 0;
            for (const chunk of this.audioBuffer) {
                combinedFloat32.set(chunk, offset);
                offset += chunk.length;
            }

            // 将 Float32Array 转换为 PCM16 ArrayBuffer（来自 OpenAI 文档）
            const pcm16Buffer = this.floatTo16BitPCM(combinedFloat32);
            const base64Audio = this.base64EncodeAudio(pcm16Buffer);

            console.log('[ATC-Chat] 正在发送 PCM 音频数据:', {
                samples: combinedFloat32.length,
                duration: combinedFloat32.length / 24000,
                pcm16_size: pcm16Buffer.byteLength,
                base64_length: base64Audio.length
            });

            // 以 OpenAI 实时 API 格式发送
            const message = {
                type: 'input_audio_buffer.append',
                audio: base64Audio
            };

            if (this.websocket && this.websocket.readyState === WebSocket.OPEN) {
                this.websocket.send(JSON.stringify(message));

                // 提交音频缓冲区
                this.websocket.send(JSON.stringify({
                    type: 'input_audio_buffer.commit'
                }));

                // 创建响应（无指令 - 使用会话级别的指令）
                this.websocket.send(JSON.stringify({
                    type: 'response.create',
                    response: {
                        modalities: ['text', 'audio']
                        // 已移除指令以允许会话级别的指令生效
                    }
                }));
            }

            // 清空缓冲区
            this.audioBuffer = [];

        } catch (error) {
            console.error('[ATC-Chat] 转换并发送 PCM 音频失败:', error);
        }
    }

    // 将音频数据的 Float32Array 转换为 PCM16 ArrayBuffer（来自 OpenAI 文档）
    floatTo16BitPCM(float32Array) {
        const buffer = new ArrayBuffer(float32Array.length * 2);
        const view = new DataView(buffer);
        let offset = 0;
        for (let i = 0; i < float32Array.length; i++, offset += 2) {
            let s = Math.max(-1, Math.min(1, float32Array[i]));
            view.setInt16(offset, s < 0 ? s * 0x8000 : s * 0x7fff, true);
        }
        return buffer;
    }

    // 将 ArrayBuffer 转换为 base64 编码的字符串（来自 OpenAI 文档）
    base64EncodeAudio(arrayBuffer) {
        let binary = '';
        let bytes = new Uint8Array(arrayBuffer);
        const chunkSize = 0x8000; // 32KB 块大小
        for (let i = 0; i < bytes.length; i += chunkSize) {
            let chunk = bytes.subarray(i, i + chunkSize);
            binary += String.fromCharCode.apply(null, chunk);
        }
        return btoa(binary);
    }

    queueAudioData(base64Audio) {
        // 将 base64 音频数据排入队列以便播放
        this.audioQueue.push(base64Audio);
    }

    queueAudioBlob(blob) {
        // 将音频 blob 排入队列以便播放
        this.audioQueue.push(blob);
    }

    async playQueuedAudio() {
        if (this.audioQueue.length === 0) {
            console.log('[ATC-Chat] 队列中没有要播放的音频');
            return;
        }

        if (this.isPlaying) {
            console.log('[ATC-Chat] 音频已在播放，跳过新音频');
            return;
        }

        this.isPlaying = true;
        this.showStatusIndicator('playing', 'AI 正在说话...');

        try {
            // 单独解码每个 base64 块并合并二进制数据
            let totalLength = 0;
            const binaryChunks = [];

            for (const base64Chunk of this.audioQueue) {
                try {
                    // 清理并解码每个 base64 块
                    const cleanBase64 = base64Chunk.replace(/[^A-Za-z0-9+/=]/g, '');
                    const paddedBase64 = cleanBase64 + '='.repeat((4 - cleanBase64.length % 4) % 4);
                    const binaryString = atob(paddedBase64);
                    binaryChunks.push(binaryString);
                    totalLength += binaryString.length;
                } catch (error) {
                    console.warn('[ATC-Chat] 解码 base64 块失败:', error);
                }
            }

            // 立即清空队列以防止重新播放
            this.audioQueue = [];

            console.log('[ATC-Chat] 正在播放来自', binaryChunks.length, '个块的合并音频，总字节:', totalLength);

            // 合并所有二进制数据
            let combinedBinary = '';
            for (const chunk of binaryChunks) {
                combinedBinary += chunk;
            }

            // 转换为 PCM16 数据
            const pcmData = new Int16Array(combinedBinary.length / 2);

            for (let i = 0; i < pcmData.length; i++) {
                const byte1 = combinedBinary.charCodeAt(i * 2);
                const byte2 = combinedBinary.charCodeAt(i * 2 + 1);
                pcmData[i] = (byte2 << 8) | byte1; // 小端序
            }

            console.log('[ATC-Chat] 正在播放 PCM 音频:', {
                samples: pcmData.length,
                duration: pcmData.length / 24000,
                size: combinedBinary.length
            });

            // 如果不存在则创建音频上下文
            if (!this.audioContext) {
                this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
                    sampleRate: 24000
                });
            }

            // 如果已暂停则恢复音频上下文
            if (this.audioContext.state === 'suspended') {
                await this.audioContext.resume();
            }

            // 创建音频缓冲区
            const audioBuffer = this.audioContext.createBuffer(1, pcmData.length, 24000);
            const channelData = audioBuffer.getChannelData(0);

            // 将 Int16 转换为 Float32 并复制到缓冲区
            for (let i = 0; i < pcmData.length; i++) {
                channelData[i] = pcmData[i] / 32768.0;
            }

            // 创建缓冲源并播放
            const source = this.audioContext.createBufferSource();
            source.buffer = audioBuffer;

            // 连接到目标设备和分析器以便可视化
            source.connect(this.audioContext.destination);
            if (this.audioAnalyser) {
                source.connect(this.audioAnalyser);
            }

            source.onended = () => {
                // 添加一个小延迟以防止音频中断
                setTimeout(() => {
                    console.log('[ATC-Chat] 音频播放结束');
                    this.isPlaying = false;
                    // 音频结束时返回到就绪状态
                    if (this.isConnected) {
                        this.showStatusIndicator('connected', '按住空格键开始 PTT');
                    }
                }, 100); // 100ms 延迟以确保完整播放
            };

            source.start();
            console.log('[ATC-Chat] 通过 Web Audio API 播放 AI 响应音频');

        } catch (error) {
            console.error('[ATC-Chat] 播放排队音频失败:', error);
            this.isPlaying = false;
            // 出错时返回到就绪状态
            if (this.isConnected) {
                this.showStatusIndicator('connected', '按住空格键开始 PTT');
            }
            // 出错时清空队列
            this.audioQueue = [];
        }
    }

    // AI 音频的音频可视化方法
    startAIAudioVisualization() {
        if (this.aiVisualizationFrameId) return;

        const renderFrame = () => {
            const visBar = document.getElementById('ai-vis-bar');
            if (!visBar) {
                if (this.aiVisualizationFrameId) {
                    cancelAnimationFrame(this.aiVisualizationFrameId);
                    this.aiVisualizationFrameId = null;
                }
                return;
            }

            // 如果可用，从当前音频上下文获取音频电平
            let audioLevel = 0;
            if (this.audioAnalyser && this.audioDataArray) {
                try {
                    this.audioAnalyser.getByteFrequencyData(this.audioDataArray);
                    let totalSum = 0;
                    let totalPoints = 0;
                    const maxBin = Math.min(this.audioDataArray.length, 40);
                    for (let j = 1; j < maxBin; j++) {
                        const weight = 1 - (j / maxBin * 0.5);
                        totalSum += this.audioDataArray[j] * weight;
                        totalPoints += weight;
                    }
                    audioLevel = totalPoints > 0 ? (totalSum / totalPoints) / 255 : 0;
                } catch (e) {
                    // 在传输/处理期间回退到模拟活动
                    if (this.isRecording || this.isPlaying) {
                        audioLevel = 0.3 + Math.random() * 0.4;
                    }
                }
            } else if (this.isRecording || this.isPlaying) {
                // 在录音或播放时模拟音频活动
                audioLevel = 0.3 + Math.random() * 0.4;
            }

            const widthPercentage = Math.min(100, audioLevel * 150);
            const currentWidth = parseFloat(visBar.style.width) || 0;
            const smoothingFactor = 0.3;
            const newWidth = (currentWidth * smoothingFactor) + (widthPercentage * (1 - smoothingFactor));

            visBar.style.width = newWidth + '%';

            this.aiVisualizationFrameId = requestAnimationFrame(renderFrame);
        };

        this.aiVisualizationFrameId = requestAnimationFrame(renderFrame);
    }

    stopAIAudioVisualization() {
        if (this.aiVisualizationFrameId) {
            cancelAnimationFrame(this.aiVisualizationFrameId);
            this.aiVisualizationFrameId = null;
        }

        const visBar = document.getElementById('ai-vis-bar');
        if (visBar) {
            visBar.style.width = '0%';
        }
    }

    // 转写管理方法
    toggleTranscriptViewer() {
        this.transcriptViewerVisible = !this.transcriptViewerVisible;
        if (this.transcriptViewerVisible) {
            this.filterTranscripts();

            // 正确定位转写查看器
            setTimeout(() => {
                const aiAdvisoryElement = document.getElementById('ai-advisory-container');
                const viewer = document.querySelector('[data-viewer-id="ai-advisory"]');

                if (aiAdvisoryElement && viewer) {
                    const rect = aiAdvisoryElement.getBoundingClientRect();
                    viewer.style.left = `${rect.left}px`;
                    viewer.style.width = `${rect.width}px`;
                    viewer.style.bottom = `${window.innerHeight - rect.top + 8}px`;
                    // 确保没有过渡或动画
                    viewer.style.transition = 'none';
                    viewer.style.transform = 'none';
                }
            }, 0);
        }
        this.triggerReactivity();
        console.log('[ATC-Chat] 转写查看器已切换:', this.transcriptViewerVisible);
    }

    addTranscript(speaker, text) {
        const transcript = {
            id: ++this.transcriptIdCounter,
            timestamp: new Date().toISOString(),
            speaker: speaker, // 'AI' 或 'PILOT'
            text: text
        };
        this.transcripts.push(transcript);

        // 确保 filteredTranscripts 立即更新
        this.filterTranscripts();

        // 触发 Alpine.js 响应式
        this.triggerReactivity();

        // 仅保留最后 100 条转写以防止内存问题
        if (this.transcripts.length > 100) {
            this.transcripts = this.transcripts.slice(-100);
            // 修剪后重新过滤
            this.filterTranscripts();
        }
    }

    filterTranscripts() {
        // 确保数组已初始化
        if (!this.transcripts) {
            this.transcripts = [];
        }
        if (!this.filteredTranscripts) {
            this.filteredTranscripts = [];
        }

        if (!this.transcriptSearchTerm || this.transcriptSearchTerm.trim() === '') {
            this.filteredTranscripts = [...this.transcripts];
        } else {
            const searchTerm = this.transcriptSearchTerm.toLowerCase();
            this.filteredTranscripts = this.transcripts.filter(transcript =>
                transcript.text.toLowerCase().includes(searchTerm) ||
                transcript.speaker.toLowerCase().includes(searchTerm)
            );
        }
        // 按时间戳排序，最新的优先
        this.filteredTranscripts.sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
    }

    getTranscriptCount() {
        return this.transcripts ? this.transcripts.length : 0;
    }

    highlightSearchTerm(text) {
        if (!this.transcriptSearchTerm || this.transcriptSearchTerm.trim() === '') {
            return text;
        }

        const searchTerm = this.transcriptSearchTerm.trim();
        const regex = new RegExp(`(${searchTerm.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi');
        return text.replace(regex, '<mark class="bg-yellow-500/30 text-yellow-200">$1</mark>');
    }

    triggerReactivity() {
        // 通过分发自定义事件强制 Alpine.js 重新求值
        if (typeof window !== 'undefined' && window.Alpine) {
            // 触发 Alpine 可以监听的自定义事件
            document.dispatchEvent(new CustomEvent('atc-chat-update', {
                detail: {
                    isConnected: this.isConnected,
                    transcriptCount: this.getTranscriptCount(),
                    transcriptViewerVisible: this.transcriptViewerVisible,
                    transcripts: this.transcripts,
                    filteredTranscripts: this.filteredTranscripts,
                    transcriptSearchTerm: this.transcriptSearchTerm
                }
            }));
        }
    }
}

// DOM 加载时初始化 ATC Chat
document.addEventListener('DOMContentLoaded', () => {
    window.atcChat = new ATCChat();
});

// 如果可用则添加到 Alpine.js store
document.addEventListener('alpine:init', () => {
    if (window.Alpine) {
        Alpine.store('atcChat', {
            isAvailable: false,
            isConnected: false,
            sessionId: null,

            async checkAvailability() {
                try {
                    const response = await fetch(`/api/v1/config`);
                    if (response.ok) {
                        const config = await response.json();
                        this.isAvailable = config.atc_chat?.enabled || false;
                    }
                } catch (error) {
                    this.isAvailable = false;
                }
                return this.isAvailable;
            }
        });
    }
});
