/**
 * 模块: map/perf/telemetry
 * 存在原因:
 * - 为地图性能排查提供轻量的运行时遥测。
 * - 将原始计数器转换为运维友好的窗口指标。
 *
 * 主要职责:
 * - 创建并重置遥测状态。
 * - 计算滚动窗口增量及衍生的速率/百分比。
 *
 * 怪癖 / 契约:
 * - 使用 `performance.now()` 进行单调计时及稳定的短窗口统计。
 * - 期望计数器是累计的并且单调递增。
 */
(function () {
    const DEFAULT_COUNTERS = {
        refreshRequested: 0,
        refreshCoalesced: 0,
        refreshExecuted: 0,
        refreshDurationMs: 0,
        markerAdds: 0,
        markerRemoves: 0,
        labelAdds: 0,
        labelRemoves: 0,
        visualStateApplied: 0,
        visualStateSkipped: 0,
        trailLayersAdded: 0,
        trailLayersRemoved: 0,
        singleAircraftUpdates: 0,
        fullVisibilityPasses: 0,
    };

    function createTelemetry() {
        return {
            counters: { ...DEFAULT_COUNTERS },
            snapshot: {
                timestamp: performance.now(),
                counters: { ...DEFAULT_COUNTERS },
            },
        };
    }

    function getWindowStats(state) {
        const now = performance.now();
        const elapsedSeconds = Math.max(0.001, (now - state.snapshot.timestamp) / 1000);

        const delta = {};
        const keys = Object.keys(state.counters);
        for (let i = 0; i < keys.length; i++) {
            const key = keys[i];
            delta[key] = state.counters[key] - (state.snapshot.counters[key] || 0);
        }

        const refreshExecuted = delta.refreshExecuted || 0;
        const avgRefreshMs = refreshExecuted > 0 ? (delta.refreshDurationMs / refreshExecuted) : 0;
        const visualTotal = (delta.visualStateApplied || 0) + (delta.visualStateSkipped || 0);
        const visualSkipPct = visualTotal > 0 ? ((delta.visualStateSkipped / visualTotal) * 100) : 0;
        const markerOps = (delta.markerAdds || 0) + (delta.markerRemoves || 0) + (delta.labelAdds || 0) + (delta.labelRemoves || 0);
        const coalescedRatio = (delta.refreshRequested || 0) > 0 ? (delta.refreshCoalesced / delta.refreshRequested) : 0;

        const stats = {
            windowSec: Number(elapsedSeconds.toFixed(1)),
            refreshPerSec: Number((refreshExecuted / elapsedSeconds).toFixed(2)),
            avgRefreshMs: Number(avgRefreshMs.toFixed(2)),
            coalescedPct: Number((coalescedRatio * 100).toFixed(1)),
            markerOpsPerSec: Number((markerOps / elapsedSeconds).toFixed(2)),
            singleUpdatesPerSec: Number(((delta.singleAircraftUpdates || 0) / elapsedSeconds).toFixed(2)),
            visualSkipPct: Number(visualSkipPct.toFixed(1)),
            trailLayerOpsPerSec: Number((((delta.trailLayersAdded || 0) + (delta.trailLayersRemoved || 0)) / elapsedSeconds).toFixed(2)),
            fullVisibilityPasses: delta.fullVisibilityPasses || 0,
        };

        state.snapshot = {
            timestamp: now,
            counters: { ...state.counters },
        };

        return stats;
    }

    window.MapTelemetry = {
        createTelemetry,
        getWindowStats,
    };
})();
