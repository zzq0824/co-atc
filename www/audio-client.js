/**
 * Co-ATC 的音频流客户端。
 *
 * 职责：
 * - 准备和管理每个频率的 HTMLAudioElement 流。
 * - 维护用于 UI 可视化的 Web Audio 分析器管线。
 * - 提供静音/取消静音和播放编排辅助方法。
 * - 跟踪每个频率自上次显著音频以来的时间。
 */
class AudioClient {
    /**
     * @param {Object} store Alpine store 实例。
     */
    constructor(store) {
        this.store = store;
        this.audioContext = null;
        this.audioElements = {};
        this.audioAnalysers = {};
        this.audioDataArrays = {};
        this.visualizationFrameIds = {};
        this.sourceNodes = {};
        this.userSetVolumes = {};
        this.lastSignificantAudioTime = {};
        this.secondsSinceLastAudio = {};

        this.mutedVolume = 0.01;
        this.defaultVolume = 1.0;
        this.visualizationTargetFps = 30;
        this.significantAudioThresholdUnmuted = 0.10;
        this.significantAudioThresholdMuted = 0.02;
        this.visualizerMultiplierUnmuted = 150;
        this.visualizerMultiplierMutedFactor = 5;
    }

    /**
     * 初始化并缓存 AudioContext。
     * @returns {AudioContext|null}
     */
    initAudioContext() {
        if (!this.audioContext) {
            try {
                this.audioContext = new (window.AudioContext || window.webkitAudioContext)();
                console.log("AudioContext 已初始化。");
            } catch (e) {
                console.error("不支持 Web Audio API。", e);
            }
        }
        return this.audioContext;
    }

    /**
     * 确保频率流已准备好元素、源 URL 和分析器管线。
     * @param {{id: string|number, stream_port?: number|string, stream_url?: string}} frequency
     */
    prepareFrequency(frequency) {
        const frequencyId = this._normalizeFrequencyId(frequency?.id);
        if (!frequencyId) {
            console.error('prepareFrequency: 缺少 frequency.id');
            return;
        }

        if (this.audioElements[frequencyId]?.element) {
            return;
        }

        console.log(`正在准备频率: ${frequencyId}`);
        this.initAudioContext();

        const audioElement = document.createElement('audio');
        audioElement.crossOrigin = 'anonymous';
        audioElement.preload = 'metadata';
        audioElement.playsInline = true;

        this.userSetVolumes[frequencyId] = this.userSetVolumes[frequencyId] || this.defaultVolume;

        const streamUrl = this._buildStreamUrl(frequency);
        if (!streamUrl) {
            console.error(`prepareFrequency: ${frequencyId} 的流 URL 无效`);
            return;
        }

        audioElement.addEventListener('error', (e) => {
            const message = e.target && e.target.error ? e.target.error.message : '未知错误';
            console.error(`${frequencyId} 的音频错误:`, message);
        });
        audioElement.addEventListener('playing', () => {
            if (!this.visualizationFrameIds[frequencyId]) {
                this.startVisualization(frequencyId);
            }
        });
        audioElement.addEventListener('pause', () => {
            this.cleanupVisualization(frequencyId);
        });

        this.audioElements[frequencyId] = {
            element: audioElement,
            intendedSrc: streamUrl,
            isPrepared: true
        };

        this.setupVisualization(frequencyId, audioElement);
    }

    /**
     * 连接并启动已准备好的频率流的播放。
     * @param {{id: string|number}} frequency
     */
    connectToFrequency(frequency) {
        const frequencyId = this._normalizeFrequencyId(frequency?.id);
        if (!frequencyId) {
            console.error('connectToFrequency: 缺少 frequency.id');
            return;
        }

        if (!this.audioElements[frequencyId]?.element) {
            console.warn(`连接期间未找到 ${frequencyId} 的音频元素。正在准备。`);
            this.prepareFrequency(frequency);
        }

        const audioInfo = this.audioElements[frequencyId];
        if (!audioInfo || !audioInfo.element || !audioInfo.intendedSrc) {
            console.error(`频率 ${frequencyId} 的音频信息不完整。无法连接。`);
            return;
        }

        const audioElement = audioInfo.element;
        const intendedSrc = audioInfo.intendedSrc;

        audioElement.volume = this.store.unmutedFrequencies.has(frequencyId)
            ? (this.userSetVolumes[frequencyId] || this.defaultVolume)
            : this.mutedVolume;

        if (audioElement.currentSrc !== intendedSrc) {
            audioElement.src = intendedSrc;
        }

        if (!this.visualizationFrameIds[frequencyId] && this.audioAnalysers[frequencyId]) {
            this.startVisualization(frequencyId);
        }

        if (audioElement.readyState === 0 || audioElement.currentSrc !== intendedSrc) {
            audioElement.load();
        }

        if (audioElement.paused) {
            const playPromise = audioElement.play();

            if (playPromise !== undefined) {
                playPromise.catch(error => {
                    if (error.name !== 'AbortError') {
                        console.error(`调用 ${frequencyId} 的播放时出错:`, error);
                    }
                });
            }
        }
    }

    /**
     * 在所有已知无线电频率上启动播放尝试。
     */
    startAllRadios() {
        if (this.store.radiosStarted) {
            return;
        }

        this.store.radiosStarted = true;
        const context = this.initAudioContext();
        if (!context) {
            console.error('startAllRadios: AudioContext 不可用');
            return;
        }

        const resumeContextAndPlay = () => {
            this.store.audioFrequencies.forEach(freq => {
                this.connectToFrequency(freq);
            });
        };

        if (context.state === 'suspended') {
            context.resume().then(() => {
                resumeContextAndPlay();
            }).catch(e => {
                console.error("恢复音频上下文时出错:", e);
                resumeContextAndPlay();
            });
        } else {
            resumeContextAndPlay();
        }
    }

    /**
     * 为指定频率创建分析器管线。
     * @param {string} frequencyId
     * @param {HTMLAudioElement} audioElement
     */
    setupVisualization(frequencyId, audioElement) {
        if (!this.audioContext) {
            return;
        }

        this._safeDisconnectNode(this.sourceNodes[frequencyId]);
        this._safeDisconnectNode(this.audioAnalysers[frequencyId]);
        delete this.sourceNodes[frequencyId];
        delete this.audioAnalysers[frequencyId];

        try {
            const sourceNode = this.audioContext.createMediaElementSource(audioElement);
            this.sourceNodes[frequencyId] = sourceNode;

            const analyserNode = this.audioContext.createAnalyser();
            analyserNode.fftSize = 256;
            analyserNode.smoothingTimeConstant = 0.5;
            this.audioAnalysers[frequencyId] = analyserNode;

            this.audioDataArrays[frequencyId] = new Uint8Array(analyserNode.frequencyBinCount);

            sourceNode.connect(analyserNode);
            analyserNode.connect(this.audioContext.destination);
        } catch (e) {
            console.error(`为 ${frequencyId} 设置可视化时出错:`, e);
            this._safeDisconnectNode(this.sourceNodes[frequencyId]);
            this._safeDisconnectNode(this.audioAnalysers[frequencyId]);
            delete this.sourceNodes[frequencyId];
            delete this.audioAnalysers[frequencyId];
        }
    }

    /**
     * 启动频率的条形可视化动画循环。
     * @param {string} frequencyId
     */
    startVisualization(frequencyId) {
        if (this.visualizationFrameIds[frequencyId]) return;

        let lastFrameTime = 0;
        const frameInterval = 1000 / this.visualizationTargetFps;

        const renderFrame = (currentTime) => {
            if (currentTime - lastFrameTime < frameInterval) {
                this.visualizationFrameIds[frequencyId] = requestAnimationFrame(renderFrame);
                return;
            }
            lastFrameTime = currentTime;

            const analyser = this.audioAnalysers[frequencyId];
            const dataArray = this.audioDataArrays[frequencyId];

            if (!analyser || !dataArray) {
                this.cleanupVisualization(frequencyId);
                return;
            }

            try {
                analyser.getByteFrequencyData(dataArray);
            } catch (e) {
                for (let i = 0; i < dataArray.length; i++) {
                    dataArray[i] = 0;
                }
            }

            let totalSum = 0;
            let totalPoints = 0;
            const maxBin = Math.min(dataArray.length, 40);
            for (let j = 1; j < maxBin; j++) {
                const weight = 1 - (j / maxBin * 0.5);
                totalSum += dataArray[j] * weight;
                totalPoints += weight;
            }

            const audioLevel = totalPoints > 0 ? (totalSum / totalPoints) / 255 : 0;

            const isUnmuted = this.store.unmutedFrequencies.has(frequencyId);
            const significantAudioThreshold = isUnmuted
                ? this.significantAudioThresholdUnmuted
                : this.significantAudioThresholdMuted;
            const visualizerMultiplier = isUnmuted
                ? this.visualizerMultiplierUnmuted
                : (this.visualizerMultiplierUnmuted * this.visualizerMultiplierMutedFactor);

            if (audioLevel >= significantAudioThreshold) {
                this.lastSignificantAudioTime[frequencyId] = Date.now();
            } else if (!this.lastSignificantAudioTime[frequencyId]) {
                this.lastSignificantAudioTime[frequencyId] = Date.now();
            }

            const widthPercentage = Math.min(100, audioLevel * visualizerMultiplier);

            const barElement = document.getElementById(`vis-bar-${frequencyId}`);
            if (barElement) {
                const currentWidth = parseFloat(barElement.style.width) || 0;
                const smoothingFactor = 0.3;
                const newWidth = (currentWidth * smoothingFactor) + (widthPercentage * (1 - smoothingFactor));

                barElement.style.width = newWidth + '%';
                barElement.style.backgroundColor = isUnmuted ? '#4CAF50' : '#888888';

                barElement.style.opacity = '1';
            }

            this.visualizationFrameIds[frequencyId] = requestAnimationFrame(renderFrame);
        };

        this.visualizationFrameIds[frequencyId] = requestAnimationFrame(renderFrame);
    }

    /**
     * 停止频率的可视化动画。
     * @param {string} frequencyId
     */
    cleanupVisualization(frequencyId) {
        if (this.visualizationFrameIds[frequencyId]) {
            cancelAnimationFrame(this.visualizationFrameIds[frequencyId]);
            delete this.visualizationFrameIds[frequencyId];
        }
        if (this.secondsSinceLastAudio[frequencyId] !== '--s') {
            this.secondsSinceLastAudio[frequencyId] = '--s';
        }
    }

    /**
     * 切换频率的静音状态，同时保留用户设置的音量。
     * @param {{id: string|number}} frequency
     */
    toggleMute(frequency) {
        if (!this.store.radiosStarted) {
            this.startAllRadios();
        }

        const frequencyId = this._normalizeFrequencyId(frequency?.id);
        if (!frequencyId) {
            console.error('toggleMute: 缺少 frequency.id');
            return;
        }

        let audioInfo = this.audioElements[frequencyId];
        if (!audioInfo || !audioInfo.element) {
            this.prepareFrequency(frequency);
            audioInfo = this.audioElements[frequencyId];
            if (!audioInfo || !audioInfo.element) {
                console.error(`在 toggleMute 中未找到频率 ${frequencyId} 的音频元素。`);
                return;
            }
        }
        const audioElement = audioInfo.element;

        const isCurrentlyUnmuted = this.store.unmutedFrequencies.has(frequencyId);

        if (isCurrentlyUnmuted) {
            this.userSetVolumes[frequencyId] = audioElement.volume > this.mutedVolume ? audioElement.volume : this.defaultVolume;
            audioElement.volume = this.mutedVolume;
            this.store.unmutedFrequencies.delete(frequencyId);
        } else {
            audioElement.volume = this.userSetVolumes[frequencyId] || this.defaultVolume;
            this.store.unmutedFrequencies.add(frequencyId);
        }

        if (audioElement.paused && this.store.unmutedFrequencies.has(frequencyId) && this.store.radiosStarted) {
            setTimeout(() => {
                if (audioElement.paused && this.store.unmutedFrequencies.has(frequencyId)) {
                    audioElement.play().catch(err => {
                        if (err.name !== 'AbortError') {
                            console.error(`取消静音后播放 ${frequencyId} 的音频时出错:`, err);
                        }
                    });
                }
            }, 100);
        }
    }

    /**
     * 释放某频率的所有资源。
     * @param {string} frequencyId
     */
    cleanupFrequency(frequencyId) {
        this.cleanupVisualization(frequencyId);

        this._safeDisconnectNode(this.audioAnalysers[frequencyId]);
        this._safeDisconnectNode(this.sourceNodes[frequencyId]);

        delete this.audioAnalysers[frequencyId];
        delete this.sourceNodes[frequencyId];

        if (this.audioDataArrays[frequencyId]) {
            delete this.audioDataArrays[frequencyId];
        }

        if (this.audioElements[frequencyId]) {
            const audioElement = this.audioElements[frequencyId].element;
            if (audioElement) {
                audioElement.pause();
                audioElement.src = '';
            }
            delete this.audioElements[frequencyId];
        }

        delete this.lastSignificantAudioTime[frequencyId];
        delete this.secondsSinceLastAudio[frequencyId];
    }

    /**
     * 在所提供的 store 支持的对象中更新"自上次音频以来的秒数"值。
     * @param {Object} storeSecondsSinceLastAudio
     */
    updateSecondsSinceLastAudio(storeSecondsSinceLastAudio) {
        if (!storeSecondsSinceLastAudio) {
            console.warn("AudioClient: 未提供用于更新的 storeSecondsSinceLastAudio 对象。");
            return;
        }

        Object.keys(this.audioElements).forEach(frequencyId => {
            if (this.audioElements[frequencyId]?.element && this.lastSignificantAudioTime[frequencyId]) {
                const seconds = Math.floor((Date.now() - this.lastSignificantAudioTime[frequencyId]) / 1000);
                storeSecondsSinceLastAudio[frequencyId] = `${seconds}s`;
            } else if (this.audioElements[frequencyId]?.element && !this.lastSignificantAudioTime[frequencyId]) {
                storeSecondsSinceLastAudio[frequencyId] = '--s';
            }
        });
    }

    /**
     * 播放空客 retard 喊话声音。
     */
    playRetardSound() {
        if (!this.audioContext) {
            this.initAudioContext();
        }
        if (!this.audioContext) {
            console.error("无法初始化 AudioContext。无法播放 retard 声音。");
            return;
        }

        if (this.audioContext.state === 'suspended') {
            this.audioContext.resume().then(() => {
                this._playRetardSoundInternal();
            }).catch(e => {
                console.error("为 retard 声音恢复 AudioContext 时出错:", e);
            });
        } else {
            this._playRetardSoundInternal();
        }
    }

    /**
     * 播放静态 retard 声音文件的内部辅助函数。
     */
    _playRetardSoundInternal() {
        const retardSound = new Audio('/sounds/airbus_retard.mp3');
        retardSound.play()
            .catch(error => {
                console.error("播放 airbus_retard.mp3 时出错:", error);
            });
    }

    /**
     * 构建频率流 URL 并附加客户端 id。
     * @param {{stream_port?: number|string, stream_url?: string}} frequency
     * @returns {string|null}
     */
    _buildStreamUrl(frequency) {
        const streamPath = typeof frequency?.stream_url === 'string' ? frequency.stream_url : '';
        if (!streamPath) {
            return null;
        }

        const streamPort = frequency.stream_port || window.location.port;
        const base = `${window.location.protocol}//${window.location.hostname}:${streamPort}`;

        let url;
        try {
            url = new URL(streamPath, base);
        } catch {
            return null;
        }

        if (url.href.includes('CLIENT_ID')) {
            return url.href.replace('CLIENT_ID', encodeURIComponent(this.store.clientID));
        }

        if (!url.searchParams.has('id')) {
            url.searchParams.set('id', this.store.clientID);
        }

        return url.toString();
    }

    /**
     * 安全地断开 Web Audio 节点。
     * @param {AudioNode|undefined|null} node
     */
    _safeDisconnectNode(node) {
        if (!node) {
            return;
        }
        try {
            node.disconnect();
        } catch {
            // 无操作
        }
    }

    /**
     * 将频率 id 值规范化为字符串键。
     * @param {string|number|undefined|null} frequencyId
     * @returns {string|null}
     */
    _normalizeFrequencyId(frequencyId) {
        if (frequencyId === undefined || frequencyId === null) {
            return null;
        }
        return String(frequencyId);
    }
}

// 导出 AudioClient 类
window.AudioClient = AudioClient;
