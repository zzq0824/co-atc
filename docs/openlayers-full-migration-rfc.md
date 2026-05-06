# OpenLayers 完整迁移与 Leaflet 移除 RFC

**状态：** 已完成（已关闭）
**日期：** 2026-02-18
**项目：** co-atc
**范围：** 用 OpenLayers 完全替换 Leaflet，包括对地图相关前端架构的重构

---

## 1) 执行摘要

我们将完全移除 Leaflet，并将所有地图功能移植到 OpenLayers。

这不是 1:1 的库交换。我们将利用本次迁移把地图代码重构为可维护的模块，将飞行器渲染迁至 WebGL 优先的路径，并通过一致的层注册表正式化地图覆盖层（航空层、雷达天气、空域环、参考数据覆盖）。

完成时，必须**对 Leaflet 零运行时依赖**、达到完整功能对等，并在高飞行器负载下展现可衡量的性能改进或稳定性。

### 迁移结案摘要（已执行）

- Leaflet 运行时和地图启动路径已完全移除；OpenLayers 现在是唯一的地图引擎。
- 地图架构在 `www/map/` 下被模块化，分为专门的 core、renderer、feature 和 telemetry 模块。
- 飞行器渲染、轨迹渲染、标签、选中/悬停状态、邻近行为以及 mini-map 已被移植到 OpenLayers。
- 关键覆盖层（空域/天气）的 TAR1090 风格源对等工作已完成，具有运行时故障处理和每层控件。
- 地图控件 UX 整合至地图内弹出控件，提供层切换/不透明度和飞行器显示控件。
- 平滑/预测管道已重新连线至基于 OpenLayers 特征运行（不再依赖遗留 marker），并进行了抗抖动调优和姿态冲突修复。
- 遥测/调试面板现在报告与模式相符的统计数据（插值 FPS 与有效更新速率）。
- API/websocket 精度策略已为 track/prediction/map 运动一致性进行了标准化（GPS 6 位小数；alt/speed/heading/rates 使用整数）。

---

## 2) 为何进行此次迁移

### 关键驱动因素
- 更好的高密度渲染架构（WebGL 点路径）
- 面向航空使用场景的一流覆盖层模型（航图、天气、空域）
- 比继续优化深度依赖 Leaflet 的代码具有更清晰的长期可维护性
- 减少 `www/map-manager.js` 中技术债的机会

### 决策
- **不长期支持双引擎**
- 临时功能开关仅在迁移期间使用，之后将被移除

---

## 3) 目标 / 非目标

### 目标
1. 从 UI 运行时和源代码中移除 Leaflet。
2. 保留现有的用户可见地图行为（选择、轨迹、标签、过滤、mini-map、覆盖层）。
3. 引入 OpenLayers WebGL 飞行器渲染路径。
4. 将地图代码重构为模块化架构。
5. 添加结构化覆盖层注册表和源策略控件。

### 非目标
- 重写后端 API
- 与地图行为无关的 UI 重新设计
- 地图迁移范围之外的新无关特性

---

## 4) 当前状态（受影响代码）

主要的当前地图集成：
- `www/index.html`（Leaflet CSS/JS 引入）
- `www/app.js`（MapManager 接线 + 地图触发的 store 交互）
- `www/map-manager.js`（主要地图生命周期、渲染、交互、轨迹、覆盖层、性能）
- `www/aircraft-animation.js`（与地图 marker 状态交互）
- `www/style.css`（如有 Leaflet 特定选择器）
- `docs/technical_docs.md` 等处提及 Leaflet 的文档

当前地图代码功能丰富但是单体的；迁移成功需要先进行分解。

---

## 5) 目标架构

在 `www/map/` 下创建地图模块：

- `www/map/core/map-engine.js`
  - OpenLayers 地图初始化/销毁、视图操作、通用辅助函数
- `www/map/core/layer-registry.js`
  - 所有层 + 元数据（z-index、可见性、不透明度、源策略）的中央声明
- `www/map/renderers/aircraft-webgl.js`
  - 通过 WebGL 点/矢量源更新进行飞行器位置渲染
- `www/map/renderers/trails.js`
  - 历史、回溯、未来、选中飞行器路径渲染
- `www/map/renderers/labels.js`
  - 标签文本、状态转换、过期更新、去拥挤行为
- `www/map/renderers/reference.js`
  - 机场/直升机场/导航台/跑道/全部跑道/环
- `www/map/features/proximity.js`
  - 邻近圆 + 集合成员行为
- `www/map/features/interactions.js`
  - 单击/双击/选中/站点单击模式
- `www/map/features/minimap.js`
  - 飞行器详情 mini-map
- `www/map/perf/telemetry.js`
  - 性能计数器 + 报告 API（与当前地图性能面板对等）
- `www/map/openlayers-map-manager.js`
  - 替代 `MapManager` 单体的协调器，提供 `app.js` 期望的 API

在切实可行处保持外部 app 契约稳定：
- `initMap()`
- `applyFiltersAndRefreshView()`
- `updateSingleAircraft()`
- `toggleRings()`
- `app.js` 中的其他现有调用点

---

## 6) 迁移阶段（执行计划）

## 阶段 0 — 基线与冻结（2-3 天）

### 任务
- 使用当前地图性能统计捕获基线指标：
  - 平均刷新毫秒数
  - 每秒刷新次数
  - 每秒 marker 操作数
  - 高飞行器数量下的行为
- 在迁移期间冻结新地图功能工作。
- 定义对等清单和验收阈值。

### 退出标准
- 基线指标已捕获，并在迁移执行期间进行比较。
- 对等清单已批准。

---

## 阶段 1 — 重构准备（4-6 天）

### 任务
- 在不改变行为的情况下将 `www/map-manager.js` 拆分为多个模块。
- 将状态派生与渲染副作用分离。
- 引入层注册表抽象和地图事件总线。

### 退出标准
- 现有 Leaflet 行为不变。
- 地图代码组织在模块边界中。
- 无用户可见回归。

---

## 阶段 2 — OpenLayers 基础（4-5 天）

### 任务
- 在前端添加 OpenLayers 依赖和加载路径。
- 实现地图创建、底图层、视图控件、移动/缩放事件处理器。
- 移植地图单击 + 双击语义和站点覆盖单击模式。
- 添加兼容垫片，使 app store 仍然以不变的方式调用 manager API。

### 退出标准
- 应用启动时 OpenLayers 地图处于活动状态。
- 核心交互工作（平移/缩放/单击）。
- 启动路径中没有 Leaflet 对象的使用。

---

## 阶段 3 — 飞行器渲染（WebGL 优先）（7-10 天）

### 任务
- 将飞行器渲染实现为 OpenLayers WebGL 点或同等高性能矢量路径。
- 移植 marker 状态更新（位置/航向/状态/选中高亮）。
- 移植单飞行器更新快速路径。
- 保留视口逻辑和过滤语义。

### 退出标准
- 实现飞行器渲染/更新对等。
- 与基线相比，高负载响应性持平或更佳。

---

## 阶段 4 — 轨迹、标签、选择、邻近（7-10 天）

### 任务
- 移植轨迹（实时 + 历史 + 回溯 + 未来）。
- 移植标签渲染/刷新逻辑和淡入淡出/视觉状态转换。
- 移植选中飞行器行为和表格悬停联动。
- 移植邻近圆 + 集合过滤行为。

### 退出标准
- 选中飞行器 UX 对等完成。
- 轨迹可视化匹配基线行为。

---

## 阶段 5 — 参考层 + 航空覆盖层 + Mini-Map（7-10 天）

### 任务
- 移植机场、直升机场、导航台、跑道、全部跑道、距离环。
- 添加结构化覆盖层支持：
  - 航图层
  - 雷达/云天气层
  - 空域多边形/环
- 移植详情 mini-map 行为。
- 为每个覆盖层数据源添加独立故障处理。

### 退出标准
- 覆盖层切换可操作且相互隔离。
- Mini-map 完全功能正常。
- 启用覆盖层时飞行器渲染保持稳定。

---

## 阶段 6 — Leaflet 移除与硬清理（3-4 天）

### 任务
- 从 `www/index.html` 移除 Leaflet CSS/JS 引用。
- 移除 Leaflet 特定代码和死的兼容分支。
- 移除 Leaflet 特定样式/选择器。
- 更新文档和架构引用。

### 退出标准
- 整个仓库中零 Leaflet 导入/使用。
- 仅使用 OpenLayers 即可构建/运行。
- 文档已更新。

---

## 阶段 7 — 稳定化、回归、签核（3-5 天）

### 任务
- 运行完整回归清单。
- 运行高负载会话并与基线比较。
- 仅修复迁移回归。
- 最终通过/不通过审查。

### 退出标准
- 对等清单全部绿色。
- 性能门关通过。
- RFC 标记为已完成。

---

## 7) 文件级工作板

## 核心前端
- `www/index.html`
  - 移除 Leaflet 引入
  - 添加 OpenLayers 引入/初始化要求
- `www/app.js`
  - 重新连线 manager 创建/导入
  - 切实可行时保持公共地图 manager 契约稳定
- `www/map-manager.js`
  - 分解，然后由 `www/map/openlayers-map-manager.js` 替换
- `www/aircraft-animation.js`
  - 如果内部 marker 表示有变化，调整 marker 引用
- `www/style.css`
  - 移除 Leaflet 特定选择器/类

## 新模块（待添加）
- `www/map/core/map-engine.js`
- `www/map/core/layer-registry.js`
- `www/map/renderers/aircraft-webgl.js`
- `www/map/renderers/trails.js`
- `www/map/renderers/labels.js`
- `www/map/renderers/reference.js`
- `www/map/features/interactions.js`
- `www/map/features/proximity.js`
- `www/map/features/minimap.js`
- `www/map/perf/telemetry.js`
- `www/map/openlayers-map-manager.js`

## 文档
- `docs/technical_docs.md`（替换 Leaflet 提及）
- `README.md`（前端地图技术栈更新）
- `docs/openlayers-full-migration-rfc.md`（本文档）

---

## 8) 完成定义（严格）

所有项必须为真：
1. 不再有 Leaflet 运行时依赖或源使用。
2. 所有当前地图特性在功能上可用。
3. 高负载性能至少与基线持平，最好有所改进。
4. 覆盖层系统支持航空/雷达/空域层，并具有稳健的故障隔离。
5. 文档完全反映 OpenLayers 架构。

---

## 9) 验收清单

### 交互对等
- [x] 单击取消选中行为
- [x] 双击清除搜索行为
- [x] 站点覆盖地图单击模式
- [x] 选中飞行器在过滤后保持持续

### 渲染对等
- [x] 带航向/状态视觉的飞行器 marker
- [x] 标签开/关和过期标签刷新行为
- [x] 配置长度的轨迹和选中飞行器历史/未来覆盖层
- [x] 邻近圆和包含飞行器集合行为

### 数据覆盖层对等
- [x] 机场/直升机场/导航台切换
- [x] 跑道/全部跑道切换
- [x] 距离环切换
- [x] Mini-map 轨迹渲染

### 新覆盖层能力
- [x] 航图层
- [x] 天气雷达/云覆盖层
- [x] 空域多边形/环
- [x] 每层不透明度/可见性控件

### 性能与可靠性
- [x] 地图性能统计功能正常
- [x] 长时间运行无渐进内存增长
- [x] 高飞行器数量下平稳运行
- [x] 覆盖层数据源故障不破坏飞行器渲染

---

## 10) 风险与缓解

1. **标签对等复杂度**
   - 缓解：将标签实现为专用 renderer 模块，并附明确测试清单。

2. **WebGL 环境差异性**
   - 缓解：在 OpenLayers 中支持非 WebGL 回退渲染路径。

3. **覆盖层提供商宕机/速率限制**
   - 缓解：源级断路器和独立故障隔离。

4. **大爆炸式替换的回归风险**
   - 缓解：分阶段迁移，具有严格的阶段退出标准和门控切换。

5. **迁移期间的团队漂移**
   - 缓解：冻结无关地图功能；强制执行 RFC 范围。

---

## 11) 回滚策略

仅在迁移期间：
- 在完全删除 Leaflet 之前保留一个短期回滚分支。
- 如果在阶段 6 完成前出现严重阻塞，回退到最后一个稳定里程碑。

阶段 6 之后：
- 不再有运行时回滚到 Leaflet；修复在 OpenLayers 栈中进行。

---

## 12) 推荐项目节奏

- 第 1 周：阶段 0-1
- 第 2 周：阶段 2-3
- 第 3 周：阶段 4
- 第 4 周：阶段 5-6
- 第 5 周（缓冲）：阶段 7 稳定化

预期时长：**4-5 周**，取决于对等差距和覆盖层数据源集成工作量。

---

## 13) 执行说明

- 保持提交小且与阶段范围一致。
- 在最终清理之前保留现有公共地图 manager 方法名。
- 不要将地图迁移与无关的重构混合。
- 每个阶段必须以可运行的应用状态结束。

---

## 14) 项目启动任务（历史）

1. 创建 `migration/openlayers` 工作分支。
2. 捕获基线指标。
3. 将 `www/map-manager.js` 拆分为模块（暂不改变行为）。
4. 与当前 manager 契约并行实现 OpenLayers 初始化。

---

## 15) 责任模板（待填写）

- 迁移负责人：
- 飞行器 renderer 负责人：
- 覆盖层/层负责人：
- UI 交互负责人：
- QA/性能负责人：
- 目标切换日期：

---

## 16) 最终成功声明

当 co-atc 仅以 OpenLayers 地图基础设施发布、匹配现有的运行行为、干净地支持面向航空的覆盖层并在高飞行器流量下展现稳定性能时，本次迁移即告完成。

**关闭：** 此成功声明已达成，迁移范围已关闭。
