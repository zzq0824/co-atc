/**
 * 模块: map/features/proximity-feature
 * 存在原因:
 * - 为邻近选中状态提供一个精简的只读 API。
 * - 避免 UI/调试消费者直接耦合到管理器内部实现。
 *
 * 主要职责:
 * - 返回邻近参考目标、配置距离以及激活状态标志。
 *
 * 怪癖 / 契约:
 * - 这是一个只读门面;变更操作仍由地图管理器拥有。
 */
(function () {
    function getProximityState(manager) {
        return {
            refHex: manager.proximityRefHex,
            distanceNM: manager.proximityDistanceNM,
            hasSet: !!manager.proximityHexSet,
        };
    }

    window.MapProximityFeature = {
        getProximityState,
    };
})();
