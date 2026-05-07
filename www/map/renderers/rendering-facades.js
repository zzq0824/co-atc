/**
 * 模块: map/renderers/rendering-facades
 * 存在原因:
 * - 将多个小型渲染器门面入口合并到一个脚本中,以减少文件变动
 *   及 script 标签开销,同时保留现有的全局契约。
 *
 * 主要职责:
 * - 暴露 `window.MapAircraftRenderer.updateAircraft`。
 * - 暴露 `window.MapTrailsRenderer.refreshTrails`。
 * - 暴露 `window.MapReferenceRenderer.refreshReferenceLayers`。
 *
 * 怪癖 / 契约:
 * - 该模块刻意将所有工作委托给 `OpenLayersMapManager` 的方法。
 * - 公共全局名保持不变以避免破坏现有调用点。
 */
(function () {
    function updateAircraft(manager, hex, aircraft) {
        manager.updateSingleAircraft(hex, aircraft);
    }

    function refreshTrails(manager) {
        manager.updateFlightPaths();
    }

    function refreshReferenceLayers(manager) {
        if (manager.store.settings.showAirports) manager.renderAirports();
        if (manager.store.settings.showHeliports) manager.renderHeliports();
        if (manager.store.settings.showNavaids) manager.renderNavaids();
        if (manager.store.settings.showAllRunways) manager.renderAllRunways();
        manager.renderRunways();
    }

    window.MapAircraftRenderer = {
        updateAircraft,
    };

    window.MapTrailsRenderer = {
        refreshTrails,
    };

    window.MapReferenceRenderer = {
        refreshReferenceLayers,
    };
})();
