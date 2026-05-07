/**
 * 模块: map/features/minimap-feature
 * 存在原因:
 * - 为选中飞行器的航迹分析渲染一个聚焦的小地图视图。
 * - 将航迹可视化关注点与主地图管理器分离开来。
 *
 * 主要职责:
 * - 初始化与拆除小地图实例及其图层数据源。
 * - 绘制当前、历史、回算以及未来航迹的图元。
 * - 使小地图视图与所选飞行器的上下文保持同步。
 *
 * 怪癖 / 契约:
 * - 当容器尺寸最初不可用时使用重试循环。
 * - 当航迹视图被禁用或 OpenLayers 运行时缺失时安全空操作。
 */
(function () {
    function toMapCoordinate(lat, lon) {
        return window.ol.proj.fromLonLat([lon, lat]);
    }

    function isValidLatLon(lat, lon) {
        return typeof lat === 'number' && typeof lon === 'number' && Number.isFinite(lat) && Number.isFinite(lon);
    }

    function createPointFeature(lat, lon, style) {
        const feature = new window.ol.Feature({
            geometry: new window.ol.geom.Point(toMapCoordinate(lat, lon)),
        });
        feature.setStyle(style);
        return feature;
    }

    function createLineFeature(points, style) {
        const projected = points
            .filter((point) => Array.isArray(point) && isValidLatLon(point[0], point[1]))
            .map((point) => toMapCoordinate(point[0], point[1]));
        if (projected.length < 2) return null;

        const feature = new window.ol.Feature({
            geometry: new window.ol.geom.LineString(projected),
        });
        feature.setStyle(style);
        return feature;
    }

    function initTracksMiniMap(manager, containerId, retryCount = 0) {
        cleanupTracksMiniMap(manager);

        setTimeout(() => {
            const container = document.getElementById(containerId);
            if (!container) {
                console.warn('未找到小地图容器:', containerId);
                return;
            }

            if (container.offsetWidth === 0 || container.offsetHeight === 0) {
                if (retryCount < 5) {
                    setTimeout(() => initTracksMiniMap(manager, containerId, retryCount + 1), 500);
                } else {
                    console.error('小地图容器在 5 次重试后仍无法获取尺寸,放弃');
                }
                return;
            }

            if (!(window.ol && window.ol.Map && window.ol.View)) {
                console.warn('航迹小地图所需的 OpenLayers 运行时不可用');
                return;
            }

            try {
                manager.tracksMiniMapLayers = {
                    historical: new window.ol.source.Vector(),
                    hindcast: new window.ol.source.Vector(),
                    future: new window.ol.source.Vector(),
                    current: new window.ol.source.Vector(),
                };

                const baseLayer = new window.ol.layer.Tile({
                    source: new window.ol.source.XYZ({
                        url: 'https://{a-d}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png',
                        attributions: '&copy; OpenStreetMap contributors &copy; CARTO',
                    }),
                });

                const historicalLayer = new window.ol.layer.Vector({ source: manager.tracksMiniMapLayers.historical });
                const hindcastLayer = new window.ol.layer.Vector({ source: manager.tracksMiniMapLayers.hindcast });
                const futureLayer = new window.ol.layer.Vector({ source: manager.tracksMiniMapLayers.future });
                const currentLayer = new window.ol.layer.Vector({ source: manager.tracksMiniMapLayers.current });

                historicalLayer.setZIndex(210);
                hindcastLayer.setZIndex(220);
                futureLayer.setZIndex(230);
                currentLayer.setZIndex(240);

                const store = Alpine.store('atc');
                const initialLat = store?.selectedAircraft?.adsb?.lat;
                const initialLon = store?.selectedAircraft?.adsb?.lon;
                const center = isValidLatLon(initialLat, initialLon)
                    ? toMapCoordinate(initialLat, initialLon)
                    : toMapCoordinate(43.6777, -79.6248);

                manager.tracksMiniMap = new window.ol.Map({
                    target: containerId,
                    layers: [baseLayer, historicalLayer, hindcastLayer, futureLayer, currentLayer],
                    view: new window.ol.View({ center, zoom: 10 }),
                    controls: [],
                });
            } catch (error) {
                console.error('创建小地图时出错:', error);
                manager.tracksMiniMap = null;
                manager.tracksMiniMapLayers = null;
                return;
            }

            updateTracksMiniMap(manager);
        }, 500);
    }

    function updateTracksMiniMap(manager) {
        if (!manager.tracksMiniMap || !manager.tracksMiniMapLayers) return;

        const store = Alpine.store('atc');
        if (!store.aircraftDetailsShowHistoryView) return;

        const target = manager.tracksMiniMap.getTargetElement ? manager.tracksMiniMap.getTargetElement() : null;
        if (!target || !document.body.contains(target)) {
            manager.tracksMiniMap = null;
            manager.tracksMiniMapLayers = null;
            return;
        }

        Object.values(manager.tracksMiniMapLayers).forEach((source) => source.clear());

        const aircraft = store.selectedAircraft;
        if (!aircraft) return;

        const currentLat = aircraft?.adsb?.lat;
        const currentLon = aircraft?.adsb?.lon;
        const allPoints = [];

        if (isValidLatLon(currentLat, currentLon)) {
            allPoints.push([currentLat, currentLon]);
            const currentStyle = new window.ol.style.Style({
                image: new window.ol.style.Circle({
                    radius: 6,
                    fill: new window.ol.style.Fill({ color: aircraft.status === 'signal_lost' ? '#F44336' : '#4CAF50' }),
                    stroke: new window.ol.style.Stroke({ color: aircraft.status === 'signal_lost' ? '#F44336' : '#4CAF50', width: 2 }),
                }),
            });
            manager.tracksMiniMapLayers.current.addFeature(createPointFeature(currentLat, currentLon, currentStyle));
        }

        const historyData = store.aircraftDetailsHistoryData || [];
        const historyPoints = historyData
            .filter((position) => isValidLatLon(position?.lat, position?.lon))
            .map((position) => [position.lat, position.lon]);
        allPoints.push(...historyPoints);

        const historyLine = createLineFeature(historyPoints, new window.ol.style.Style({
            stroke: new window.ol.style.Stroke({ color: '#888888', width: 1, lineDash: [3, 3] }),
        }));
        if (historyLine) manager.tracksMiniMapLayers.historical.addFeature(historyLine);

        historyPoints.forEach((point) => {
            manager.tracksMiniMapLayers.historical.addFeature(createPointFeature(
                point[0],
                point[1],
                new window.ol.style.Style({
                    image: new window.ol.style.Circle({
                        radius: 2,
                        fill: new window.ol.style.Fill({ color: 'rgba(136,136,136,0.3)' }),
                        stroke: new window.ol.style.Stroke({ color: 'rgba(136,136,136,0.5)', width: 1 }),
                    }),
                })
            ));
        });

        const hindcastData = store.aircraftDetailsHindcastData || [];
        const hindcastPoints = hindcastData
            .filter((position) => isValidLatLon(position?.lat, position?.lon))
            .map((position) => [position.lat, position.lon]);
        allPoints.push(...hindcastPoints);

        const hindcastWithJoin = hindcastPoints.slice();
        if (hindcastWithJoin.length > 0 && historyPoints.length > 0) {
            hindcastWithJoin.push(historyPoints[0]);
        }

        const hindcastLine = createLineFeature(hindcastWithJoin, new window.ol.style.Style({
            stroke: new window.ol.style.Stroke({ color: '#00BCD4', width: 1.5, lineDash: [4, 6] }),
        }));
        if (hindcastLine) manager.tracksMiniMapLayers.hindcast.addFeature(hindcastLine);

        hindcastPoints.forEach((point) => {
            manager.tracksMiniMapLayers.hindcast.addFeature(createPointFeature(
                point[0],
                point[1],
                new window.ol.style.Style({
                    image: new window.ol.style.Circle({
                        radius: 2,
                        fill: new window.ol.style.Fill({ color: 'rgba(0,188,212,0.2)' }),
                        stroke: new window.ol.style.Stroke({ color: 'rgba(0,188,212,0.4)', width: 1 }),
                    }),
                })
            ));
        });

        const futureData = store.aircraftDetailsFutureData || [];
        const futurePoints = futureData
            .filter((position) => isValidLatLon(position?.lat, position?.lon))
            .map((position) => [position.lat, position.lon]);
        allPoints.push(...futurePoints);

        const futureLine = createLineFeature(futurePoints, new window.ol.style.Style({
            stroke: new window.ol.style.Stroke({ color: '#FFC107', width: 2, lineDash: [10, 5] }),
        }));
        if (futureLine) manager.tracksMiniMapLayers.future.addFeature(futureLine);

        futurePoints.forEach((point) => {
            manager.tracksMiniMapLayers.future.addFeature(createPointFeature(
                point[0],
                point[1],
                new window.ol.style.Style({
                    image: new window.ol.style.Circle({
                        radius: 3,
                        fill: new window.ol.style.Fill({ color: 'rgba(255,193,7,0.6)' }),
                        stroke: new window.ol.style.Stroke({ color: 'rgba(255,193,7,0.8)', width: 1 }),
                    }),
                })
            ));
        });

        if (allPoints.length > 0) {
            const view = manager.tracksMiniMap.getView();
            if (allPoints.length === 1) {
                view.setCenter(toMapCoordinate(allPoints[0][0], allPoints[0][1]));
                view.setZoom(12);
            } else {
                const projected = allPoints.map((point) => toMapCoordinate(point[0], point[1]));
                const extent = window.ol.extent.boundingExtent(projected);
                view.fit(extent, { padding: [10, 10, 10, 10], duration: 0, maxZoom: 12 });
            }
        }
    }

    function cleanupTracksMiniMap(manager) {
        if (manager.tracksMiniMap) {
            try {
                manager.tracksMiniMap.setTarget(null);
            } catch (error) {
                console.warn('清理小地图时出错:', error);
            }
            manager.tracksMiniMap = null;
            manager.tracksMiniMapLayers = null;
        }
    }

    window.MapMinimapFeature = {
        initTracksMiniMap,
        updateTracksMiniMap,
        cleanupTracksMiniMap,
    };
})();
