/**
 * Co-ATC 实时流的 WebSocket 客户端。
 *
 * 职责：
 * - 维护单个 WebSocket 连接。
 * - 将类型化的消息事件分发给本地监听器。
 * - 使用指数退避 + 抖动无限重连。
 * - 向 UI 公开连接状态和重连遥测。
 */
class WebSocketClient {
    /**
     * @param {string} url WebSocket 端点 URL。
     */
    constructor(url) {
        this.url = url;
        this.connection = null;
        this.reconnectTimeout = null;
        this.isReconnecting = false;
        this.autoReconnect = false;
        this.reconnectAttempts = 0;
        this.baseReconnectDelay = 1000;
        this.maxReconnectDelay = 30000;
        this.reconnectJitterFactor = 0.25;
        this.connectTimeoutMs = 10000;
        this.intentionalClose = false;
        this.connectionState = 'idle'; // idle | connecting | connected | reconnecting | closed
        this.nextReconnectDelayMs = null;
        this._connectTimeoutHandle = null;
        this.listeners = {
            transcription: [],
            transcription_update: [],
            aircraft: [],
            aircraft_added: [],
            aircraft_update: [],
            aircraft_predicted_state: [],
            aircraft_removed: [],
            aircraft_bulk_response: [],
            status_update: [],
            phase_change: [],
            clearance_issued: [],
            frequency_status: [],
            state_change: [],
            reconnect_scheduled: [],
            open: [],
            close: [],
            error: []
        };

        this._boundOpenHandler = null;
        this._boundCloseHandler = null;
        this._boundErrorHandler = null;
        this._boundMessageHandler = null;

        this._messageCounters = {
            total: 0,
            parseErrors: 0,
            byType: {}
        };
        this._messageSnapshot = {
            timestamp: performance.now(),
            counters: {
                total: 0,
                parseErrors: 0,
                byType: {}
            }
        };
    }

    /**
     * 连接（或重新连接）套接字。
     *
     * 行为：
     * - 清除待处理的重连计时器。
     * - 防止并发的连接尝试。
     * - 替换任何现有的套接字实例。
     * - 为停滞的 CONNECTING 状态启动看门狗超时。
     */
    connect() {
        this.intentionalClose = false;

        if (this.reconnectTimeout) {
            clearTimeout(this.reconnectTimeout);
            this.reconnectTimeout = null;
        }
        this.nextReconnectDelayMs = null;

        if (this.isReconnecting) {
            console.log('WebSocket: 连接尝试已在进行中');
            return;
        }

        if (this.connection) {
            this._removeConnectionHandlers();
            this.connection.close();
            this.connection = null;
        }

        this.isReconnecting = true;
        this._setConnectionState('connecting');

        this.connection = new WebSocket(this.url);

        this._clearConnectTimeout();
        this._connectTimeoutHandle = setTimeout(() => {
            if (this.connection && this.connection.readyState === WebSocket.CONNECTING) {
                console.warn('WebSocket: 已达到连接超时，强制重连');
                try {
                    this.connection.close();
                } catch (error) {
                    console.error('WebSocket: 关闭超时套接字失败', error);
                }
            }
        }, this.connectTimeoutMs);

        this._boundOpenHandler = (event) => {
            console.log('WebSocket 连接已建立');
            this.isReconnecting = false;
            this.reconnectAttempts = 0;
            this.nextReconnectDelayMs = null;
            this._clearConnectTimeout();
            this._setConnectionState('connected');
            this._notifyListeners('open', event);
        };

        this._boundCloseHandler = (event) => {
            console.log('WebSocket 连接已关闭');
            this.isReconnecting = false;
            this._clearConnectTimeout();
            this._notifyListeners('close', event);

            if (this.autoReconnect && !this.intentionalClose) {
                this.reconnectAttempts++;
                const delayMs = this._getReconnectDelayMs();
                this.nextReconnectDelayMs = delayMs;
                this._setConnectionState('reconnecting');
                this._notifyListeners('reconnect_scheduled', {
                    attempt: this.reconnectAttempts,
                    delayMs: delayMs,
                });
                console.log(`WebSocket: 尝试第 #${this.reconnectAttempts} 次重连，将在 ${delayMs}ms 后进行`);

                this.reconnectTimeout = setTimeout(() => {
                    this.reconnectTimeout = null;
                    this.connect();
                }, delayMs);
            } else {
                this._setConnectionState('closed');
            }
        };

        this._boundErrorHandler = (event) => {
            console.error('WebSocket 错误:', event);
            this._notifyListeners('error', event);
        };

        this._boundMessageHandler = (event) => {
            try {
                const message = JSON.parse(event.data);
                this._recordInboundMessage(message?.type || 'unknown');

                if (message.type === 'aircraft_added') {
                    this._notifyListeners('aircraft_added', message.data);
                } else if (message.type === 'aircraft_update') {
                    this._notifyListeners('aircraft_update', message.data);
                } else if (message.type === 'aircraft_predicted_state') {
                    this._notifyListeners('aircraft_predicted_state', message.data);
                } else if (message.type === 'aircraft_removed') {
                    this._notifyListeners('aircraft_removed', message.data);
                } else if (message.type === 'aircraft_bulk_response') {
                    this._notifyListeners('aircraft_bulk_response', message.data);
                } else if (message.type === 'transcription') {
                    this._notifyListeners('transcription', message.data);
                } else if (message.type === 'transcription_update') {
                    this._notifyListeners('transcription_update', message.data);
                } else if (message.type === 'aircraft') {
                    if (message.data && message.data.movement) {
                        if (window.Alpine && Alpine.store('atc')) {
                            Alpine.store('atc').handleAircraftMessage(message.data);
                        }
                    }
                    this._notifyListeners('aircraft', message.data);
                } else if (message.type === 'status_update') {
                    if (window.Alpine && Alpine.store('atc')) {
                        Alpine.store('atc').handleStatusUpdateMessage(message.data);
                    }
                    this._notifyListeners('status_update', message.data);
                } else if (message.type === 'phase_change') {
                    this._notifyListeners('phase_change', message.data);
                } else if (message.type === 'frequency_status') {
                    this._notifyListeners('frequency_status', message.data);
                }
            } catch (error) {
                this._recordParseError();
                console.error('解析 WebSocket 消息时出错:', error);
            }
        };

        this.connection.addEventListener('open', this._boundOpenHandler);
        this.connection.addEventListener('close', this._boundCloseHandler);
        this.connection.addEventListener('error', this._boundErrorHandler);
        this.connection.addEventListener('message', this._boundMessageHandler);
    }

    /**
     * 使用带抖动的有上限指数退避计算下一次重连延迟。
     * @returns {number} 延迟（毫秒）。
     */
    _getReconnectDelayMs() {
        const exponent = Math.max(0, this.reconnectAttempts-1);
        const exponentialDelay = this.baseReconnectDelay * Math.pow(2, exponent);
        const cappedDelay = Math.min(this.maxReconnectDelay, exponentialDelay);
        const jitterRange = cappedDelay * this.reconnectJitterFactor;
        const jitter = (Math.random() * 2 - 1) * jitterRange;
        return Math.max(250, Math.round(cappedDelay + jitter));
    }

    /**
     * 更新连接状态，并在状态变化时发出 `state_change` 事件。
     * @param {'idle'|'connecting'|'connected'|'reconnecting'|'closed'} state
     */
    _setConnectionState(state) {
        if (this.connectionState === state) {
            return;
        }
        this.connectionState = state;
        this._notifyListeners('state_change', this.getConnectionStatus());
    }

    /**
     * 清除连接看门狗计时器。
     */
    _clearConnectTimeout() {
        if (this._connectTimeoutHandle) {
            clearTimeout(this._connectTimeoutHandle);
            this._connectTimeoutHandle = null;
        }
    }

    /**
     * 为 UI 和诊断快照当前的连接/重连状态。
     * @returns {{state: string, reconnectAttempts: number, nextRetryDelayMs: number|null, autoReconnect: boolean}}
     */
    getConnectionStatus() {
        return {
            state: this.connectionState,
            reconnectAttempts: this.reconnectAttempts,
            nextRetryDelayMs: this.nextReconnectDelayMs,
            autoReconnect: this.autoReconnect,
        };
    }

    /**
     * 从当前套接字分离所有事件处理程序并清除处理程序引用。
     */
    _removeConnectionHandlers() {
        if (this.connection) {
            if (this._boundOpenHandler) {
                this.connection.removeEventListener('open', this._boundOpenHandler);
            }
            if (this._boundCloseHandler) {
                this.connection.removeEventListener('close', this._boundCloseHandler);
            }
            if (this._boundErrorHandler) {
                this.connection.removeEventListener('error', this._boundErrorHandler);
            }
            if (this._boundMessageHandler) {
                this.connection.removeEventListener('message', this._boundMessageHandler);
            }
        }

        this._boundOpenHandler = null;
        this._boundCloseHandler = null;
        this._boundErrorHandler = null;
        this._boundMessageHandler = null;

        this._clearConnectTimeout();
    }

    /**
     * 主动关闭连接并禁用自动重连。
     */
    disconnect() {
        this.autoReconnect = false;
        this.intentionalClose = true;
        this.nextReconnectDelayMs = null;

        if (this.reconnectTimeout) {
            clearTimeout(this.reconnectTimeout);
            this.reconnectTimeout = null;
        }

        this._removeConnectionHandlers();

        if (this.connection) {
            this.connection.close();
            this.connection = null;
        }

        this.isReconnecting = false;
        this._setConnectionState('idle');
    }

    /**
     * 移除每种事件类型的所有已注册监听器。
     */
    clearAllListeners() {
        Object.keys(this.listeners).forEach(type => {
            this.listeners[type] = [];
        });
    }

    /**
     * 在非主动断开连接后启用自动重连。
     */
    enableAutoReconnect() {
        this.autoReconnect = true;
        this.intentionalClose = false;
    }

    /**
     * 禁用自动重连。
     */
    disableAutoReconnect() {
        this.autoReconnect = false;
    }

    /**
     * 重置重连遥测计数器。
     */
    resetReconnectAttempts() {
        this.reconnectAttempts = 0;
        this.nextReconnectDelayMs = null;
    }

    /**
     * 为受支持的消息/事件类型注册事件监听器。
     * @param {string} type
     * @param {(data: any) => void} callback
     */
    addEventListener(type, callback) {
        if (this.listeners[type]) {
            this.listeners[type].push(callback);
        }
    }

    /**
     * 移除先前注册的监听器回调。
     * @param {string} type
     * @param {(data: any) => void} callback
     */
    removeEventListener(type, callback) {
        if (this.listeners[type]) {
            this.listeners[type] = this.listeners[type].filter(cb => cb !== callback);
        }
    }

    /**
     * 从服务器请求批量飞行器数据负载。
     * @param {Object} [filters={}] 可选的服务器端过滤对象。
     */
    requestBulkAircraftData(filters = {}) {
        if (this.connection && this.connection.readyState === WebSocket.OPEN) {
            const message = {
                type: 'aircraft_bulk_request',
                data: {
                    filters: filters
                }
            };

            console.log('正在通过 WebSocket 请求批量飞行器数据...', filters);
            this.connection.send(JSON.stringify(message));
        } else {
            console.error('WebSocket 未连接，无法请求批量数据');
        }
    }

    /**
     * 向所有订阅者发出内部事件。
     * @param {string} type
     * @param {any} data
     */
    _notifyListeners(type, data) {
        if (this.listeners[type]) {
            this.listeners[type].forEach(callback => {
                try {
                    callback(data);
                } catch (error) {
                    console.error(`${type} 监听器中出错:`, error);
                }
            });
        }
    }

    _recordInboundMessage(type) {
        this._messageCounters.total += 1;
        const key = type || 'unknown';
        this._messageCounters.byType[key] = (this._messageCounters.byType[key] || 0) + 1;
    }

    _recordParseError() {
        this._messageCounters.parseErrors += 1;
    }

    getMessageRateStats() {
        const now = performance.now();
        const elapsedSeconds = Math.max(0.001, (now - this._messageSnapshot.timestamp) / 1000);

        const current = this._messageCounters;
        const previous = this._messageSnapshot.counters;

        const deltaTotal = current.total - (previous.total || 0);
        const deltaParseErrors = current.parseErrors - (previous.parseErrors || 0);

        const allTypes = new Set([
            ...Object.keys(current.byType || {}),
            ...Object.keys(previous.byType || {})
        ]);

        const byTypePerSec = {};
        allTypes.forEach((type) => {
            const currentCount = current.byType[type] || 0;
            const previousCount = previous.byType[type] || 0;
            const delta = currentCount - previousCount;
            byTypePerSec[type] = Number((delta / elapsedSeconds).toFixed(2));
        });

        this._messageSnapshot = {
            timestamp: now,
            counters: {
                total: current.total,
                parseErrors: current.parseErrors,
                byType: { ...current.byType }
            }
        };

        return {
            windowSec: Number(elapsedSeconds.toFixed(1)),
            totalPerSec: Number((deltaTotal / elapsedSeconds).toFixed(2)),
            parseErrorsPerSec: Number((deltaParseErrors / elapsedSeconds).toFixed(2)),
            byTypePerSec
        };
    }
}

// 导出 WebSocketClient 类
window.WebSocketClient = WebSocketClient;
