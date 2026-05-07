/**
 * 模块: map/core/event-bus
 * 存在原因:
 * - 为地图内部提供一个微型的、无依赖的发布/订阅原语。
 * - 避免需要轻量信号传递的地图要素之间产生紧耦合。
 *
 * 主要职责:
 * - 注册 (`on`) 与注销 (`off`) 事件处理器。
 * - 安全地发布载荷 (`emit`),并支持完全销毁 (`clear`)。
 *
 * 怪癖 / 契约:
 * - 处理器异常会被捕获并记录,以避免一个故障订阅者
 *   影响其他订阅者的事件投递。
 * - 实现为挂载到 `window.MapEventBus` 上的全局工厂,以匹配现有的
 *   非打包前端模块加载方式。
 */
(function () {
    function createEventBus() {
        const listeners = new Map();

        function on(eventName, handler) {
            if (!listeners.has(eventName)) {
                listeners.set(eventName, new Set());
            }
            listeners.get(eventName).add(handler);
            return () => off(eventName, handler);
        }

        function off(eventName, handler) {
            const handlers = listeners.get(eventName);
            if (!handlers) return;
            handlers.delete(handler);
            if (handlers.size === 0) {
                listeners.delete(eventName);
            }
        }

        function emit(eventName, payload) {
            const handlers = listeners.get(eventName);
            if (!handlers) return;
            handlers.forEach((handler) => {
                try {
                    handler(payload);
                } catch (error) {
                    console.warn('MapEventBus 处理器错误:', error);
                }
            });
        }

        function clear() {
            listeners.clear();
        }

        return { on, off, emit, clear };
    }

    window.MapEventBus = {
        createEventBus,
    };
})();
