/**
 * 模块: map/core/visibility-rules
 * 存在原因:
 * - 集中存放地图刷新/更新路径所使用的飞行器可见性判定。
 * - 确保完整遍历与增量更新的过滤语义保持一致。
 *
 * 主要职责:
 * - 评估搜索、地面/空中状态、高度、阶段以及最近活跃过滤条件。
 * - 即使选中飞行器不再匹配过滤条件,也保留其可见性。
 *
 * 怪癖 / 契约:
 * - 当阶段不可用时默认为 `NEW`。
 * - 调用者可以通过 `options.isInViewport = false` 绕过视口门控。
 */
(function () {
    function getCurrentPhase(aircraft) {
        return (aircraft && aircraft.phase && aircraft.phase.current && aircraft.phase.current.length > 0)
            ? aircraft.phase.current[0].phase
            : 'NEW';
    }

    function matchesSearch(aircraft, searchTerm) {
        const searchLower = (searchTerm || '').toLowerCase();
        if (searchLower === '') return true;

        const callsign = (aircraft.flight || aircraft.hex || '').toLowerCase();
        const type = (aircraft.adsb?.type || '').toLowerCase();
        const category = (aircraft.adsb?.category || '').toLowerCase();
        const typeCode = (aircraft.type_code || '').toLowerCase();
        const manufacturer = (aircraft.manufacturer || '').toLowerCase();

        const bsdbType = (
            aircraft.bsdb &&
            aircraft.bsdb.type_desc &&
            typeof aircraft.bsdb.type_desc === 'string'
                ? aircraft.bsdb.type_desc
                : ''
        ).toLowerCase();

        return (
            callsign.includes(searchLower) ||
            type.includes(searchLower) ||
            category.includes(searchLower) ||
            typeCode.includes(searchLower) ||
            manufacturer.includes(searchLower) ||
            bsdbType.includes(searchLower)
        );
    }

    function isVisibleByGroundState(aircraft, settings) {
        return (aircraft.on_ground && settings.showGroundAircraft) ||
               (!aircraft.on_ground && settings.showAirAircraft);
    }

    function isVisibleByAltitude(aircraft, settings) {
        return aircraft.on_ground ||
            (aircraft.adsb && aircraft.adsb.alt_baro >= settings.minAltitude && aircraft.adsb.alt_baro <= settings.maxAltitude);
    }

    function isVisibleByPhase(aircraft, settings) {
        const currentPhase = getCurrentPhase(aircraft);
        return !(settings.phaseFilters && settings.phaseFilters[currentPhase] === false);
    }

    function isVisibleByLastSeen(aircraft, lastSeenCutoff) {
        if (!aircraft.last_seen) return true;
        const lastSeenDate = new Date(aircraft.last_seen);
        return lastSeenDate >= lastSeenCutoff;
    }

    function shouldShowAircraftOnMap(aircraft, store, options) {
        const matches = matchesSearch(aircraft, options.searchTerm);
        const groundVisible = isVisibleByGroundState(aircraft, store.settings);
        const altitudeVisible = isVisibleByAltitude(aircraft, store.settings);
        const phaseVisible = isVisibleByPhase(aircraft, store.settings);
        const lastSeenVisible = options.lastSeenCutoff ? isVisibleByLastSeen(aircraft, options.lastSeenCutoff) : true;
        const selected = !!(store.selectedAircraft && store.selectedAircraft.hex === aircraft.hex);
        const inViewport = options.isInViewport !== false;

        return (matches && groundVisible && altitudeVisible && phaseVisible && lastSeenVisible && inViewport) || selected;
    }

    window.MapVisibilityRules = {
        getCurrentPhase,
        matchesSearch,
        isVisibleByGroundState,
        isVisibleByAltitude,
        isVisibleByPhase,
        isVisibleByLastSeen,
        shouldShowAircraftOnMap,
    };
})();
