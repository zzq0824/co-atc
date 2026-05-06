/**
 * 模块: map/core/layer-registry
 * 存在原因:
 * - 封装非飞行器地图覆盖图层的创建与生命周期管理。
 * - 标准化覆盖层数据源处理(XYZ/WMS/GeoJSON/Vector)与隔离逻辑。
 *
 * 主要职责:
 * - 根据配置对象构建 OpenLayers 数据源/图层。
 * - 应用可见性、缩放约束、z-index、透明度以及矢量样式。
 * - 跟踪运行时状态,并对不稳定的覆盖源支持降级/重试。
 *
 * 怪癖 / 契约:
 * - 设计为具备韧性:反复失败的覆盖层会被自动禁用,
 *   而不会拖垮整个地图。
 * - 期望 `window.ol` 已存在;本模块不会延迟加载 OpenLayers。
 */
(function () {
    function createOpenLayersOverlayRegistry(map, overlayConfigs) {
        const configs = Array.isArray(overlayConfigs) ? overlayConfigs : [];
        const overlays = new Map();

        function hasOL() {
            return !!(window.ol && window.ol.layer && window.ol.source);
        }

        function toNumberOr(value, fallback) {
            return Number.isFinite(value) ? value : fallback;
        }

        function createSource(config) {
            const sourceType = (config.sourceType || '').toLowerCase();

            if (sourceType === 'xyz') {
                if (!config.url) throw new Error(`覆盖层 ${config.id}: 缺少 XYZ url`);
                return new window.ol.source.XYZ({
                    url: config.url,
                    attributions: config.attribution || undefined,
                    crossOrigin: 'anonymous',
                });
            }

            if (sourceType === 'wms') {
                if (!config.url) throw new Error(`覆盖层 ${config.id}: 缺少 WMS url`);
                return new window.ol.source.TileWMS({
                    url: config.url,
                    params: config.wmsParams || {},
                    attributions: config.attribution || undefined,
                    crossOrigin: 'anonymous',
                });
            }

            if (sourceType === 'image-wms') {
                if (!config.url) throw new Error(`覆盖层 ${config.id}: 缺少 ImageWMS url`);
                return new window.ol.source.ImageWMS({
                    url: config.url,
                    params: config.wmsParams || {},
                    attributions: config.attribution || undefined,
                    crossOrigin: 'anonymous',
                });
            }

            if (sourceType === 'geojson') {
                if (!config.url) throw new Error(`覆盖层 ${config.id}: 缺少 GeoJSON url`);
                return new window.ol.source.Vector({
                    url: config.url,
                    format: new window.ol.format.GeoJSON(),
                });
            }

            if (sourceType === 'vector') {
                return new window.ol.source.Vector();
            }

            throw new Error(`覆盖层 ${config.id}: 不支持的 sourceType ${config.sourceType}`);
        }

        function createLayer(config, source) {
            const sourceType = (config.sourceType || '').toLowerCase();
            const isTile = sourceType === 'xyz' || sourceType === 'wms';
            const isImage = sourceType === 'image-wms';

            const layer = isTile
                ? new window.ol.layer.Tile({ source })
                : isImage
                    ? new window.ol.layer.Image({ source })
                : new window.ol.layer.Vector({ source });

            if (!isTile && config.vectorStyle && window.ol?.style) {
                const fillColor = config.vectorStyle.fillColor || 'rgba(66, 153, 225, 0.2)';
                const strokeColor = config.vectorStyle.strokeColor || 'rgba(66, 153, 225, 0.9)';
                const strokeWidth = toNumberOr(config.vectorStyle.strokeWidth, 1.5);

                const textEnabled = !!config.vectorStyle.textField;
                const textField = config.vectorStyle.textField || '';
                const textColor = config.vectorStyle.textColor || '#e5e7eb';

                layer.setStyle((feature) => {
                    const geometry = feature && feature.getGeometry ? feature.getGeometry() : null;
                    const geometryType = geometry && geometry.getType ? geometry.getType() : '';

                    const image = new window.ol.style.Circle({
                        radius: toNumberOr(config.vectorStyle.pointRadius, 5),
                        fill: new window.ol.style.Fill({ color: fillColor }),
                        stroke: new window.ol.style.Stroke({ color: strokeColor, width: strokeWidth }),
                    });

                    const text = textEnabled
                        ? new window.ol.style.Text({
                            text: (feature && feature.get && feature.get(textField)) ? String(feature.get(textField)) : '',
                            font: '10px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                            offsetY: -10,
                            fill: new window.ol.style.Fill({ color: textColor }),
                            backgroundFill: new window.ol.style.Fill({ color: 'rgba(0,0,0,0.5)' }),
                            padding: [1, 3, 1, 3],
                        })
                        : undefined;

                    if (geometryType === 'Point' || geometryType === 'MultiPoint') {
                        return new window.ol.style.Style({ image, text });
                    }

                    return new window.ol.style.Style({
                        fill: new window.ol.style.Fill({ color: fillColor }),
                        stroke: new window.ol.style.Stroke({ color: strokeColor, width: strokeWidth }),
                        text,
                    });
                });
            }

            layer.setZIndex(toNumberOr(config.zIndex, 600));
            layer.setOpacity(toNumberOr(config.opacity, 1));
            layer.setVisible(config.defaultVisible === true);
            return layer;
        }

        function computeZoomVisibility(config, zoom) {
            const minZoom = toNumberOr(config.minZoom, -Infinity);
            const maxZoom = toNumberOr(config.maxZoom, Infinity);
            return zoom >= minZoom && zoom <= maxZoom;
        }

        function attachFailureIsolation(entry) {
            const isolation = entry.config.failureIsolation || {};
            const maxErrors = toNumberOr(isolation.maxErrors, 3);
            const retryMax = toNumberOr(isolation.retryMaxAttempts, 3);
            const retryBaseMs = toNumberOr(isolation.retryBaseMs, 2000);

            function markError(error) {
                entry.status.errors += 1;
                entry.status.lastError = (error && error.message) ? error.message : String(error || '覆盖层数据源错误');

                if (entry.status.errors >= maxErrors && isolation.disableOnError !== false) {
                    entry.status.degraded = true;
                    entry.layer.setVisible(false);
                }

                if (entry.status.retryAttempts >= retryMax) {
                    return;
                }

                entry.status.retryAttempts += 1;
                const delay = Math.min(30000, retryBaseMs * Math.pow(2, entry.status.retryAttempts - 1));
                window.setTimeout(() => {
                    try {
                        if (entry.source && typeof entry.source.refresh === 'function') {
                            entry.source.refresh();
                        }
                    } catch (refreshError) {
                        entry.status.lastError = (refreshError && refreshError.message) ? refreshError.message : String(refreshError);
                    }
                }, delay);
            }

            const source = entry.source;
            if (!source || typeof source.on !== 'function') return;

            const events = ['tileloaderror', 'featuresloaderror', 'imageloaderror'];
            events.forEach((eventName) => {
                source.on(eventName, (event) => {
                    markError(event);
                });
            });
        }

        function attachRefreshPolicy(entry) {
            const policy = entry.config.refreshPolicy || { type: 'none' };
            if ((policy.type || 'none') !== 'interval') return;
            const intervalMs = toNumberOr(policy.intervalMs, 60000);
            if (intervalMs < 1000) return;

            entry.refreshTimer = window.setInterval(() => {
                try {
                    if (entry.source && typeof entry.source.refresh === 'function') {
                        entry.source.refresh();
                    }
                } catch (error) {
                    entry.status.lastError = (error && error.message) ? error.message : String(error);
                }
            }, intervalMs);
        }

        function registerOverlay(config) {
            if (!hasOL() || !map || !config || !config.id) return null;

            try {
                const source = createSource(config);
                const layer = createLayer(config, source);

                const entry = {
                    id: config.id,
                    config,
                    source,
                    layer,
                    refreshTimer: null,
                    status: {
                        id: config.id,
                        visible: config.defaultVisible === true,
                        degraded: false,
                        errors: 0,
                        retryAttempts: 0,
                        lastError: null,
                    },
                };

                map.addLayer(layer);
                attachFailureIsolation(entry);
                attachRefreshPolicy(entry);

                overlays.set(config.id, entry);
                return entry;
            } catch (error) {
                console.warn('[MapLayerRegistry] 覆盖层注册失败:', config.id, error);
                return null;
            }
        }

        function registerAll() {
            configs.forEach((config) => registerOverlay(config));
            return getStatuses();
        }

        function setVisible(id, visible) {
            const entry = overlays.get(id);
            if (!entry) return;
            entry.layer.setVisible(!!visible);
            entry.status.visible = !!visible;
        }

        function setOpacity(id, opacity) {
            const entry = overlays.get(id);
            if (!entry) return;
            const value = Math.max(0, Math.min(1, toNumberOr(opacity, 1)));
            entry.layer.setOpacity(value);
            entry.config.opacity = value;
        }

        function updateZoomVisibility(zoom) {
            overlays.forEach((entry) => {
                const inZoom = computeZoomVisibility(entry.config, zoom);
                const requestedVisible = entry.status.visible;
                entry.layer.setVisible(requestedVisible && inZoom && !entry.status.degraded);
            });
        }

        function getStatuses() {
            const statuses = [];
            overlays.forEach((entry) => {
                statuses.push({
                    id: entry.id,
                    visible: entry.status.visible,
                    degraded: entry.status.degraded,
                    errors: entry.status.errors,
                    retryAttempts: entry.status.retryAttempts,
                    lastError: entry.status.lastError,
                    opacity: entry.config.opacity,
                });
            });
            return statuses;
        }

        function dispose() {
            overlays.forEach((entry) => {
                if (entry.refreshTimer) {
                    clearInterval(entry.refreshTimer);
                    entry.refreshTimer = null;
                }
                if (map && entry.layer) {
                    map.removeLayer(entry.layer);
                }
                if (entry.source && typeof entry.source.clear === 'function') {
                    entry.source.clear();
                }
            });
            overlays.clear();
        }

        return {
            registerAll,
            setVisible,
            setOpacity,
            updateZoomVisibility,
            getStatuses,
            dispose,
        };
    }

    window.MapLayerRegistry = {
        createOpenLayersOverlayRegistry,
    };
})();
