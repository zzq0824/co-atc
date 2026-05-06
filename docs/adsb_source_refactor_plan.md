# ADS-B 数据源重构计划（请先审阅）

## 目标

重构 ADS-B 数据源配置和启动行为，以支持四种显式数据源类型、更智能的源处理、readsb 自动检测以及向前端暴露源元数据。

本文档仅作计划用途，**尚未**实施代码变更。

## 请求模式（已规范化）

实现将支持以下 `adsb.source_type` 值：

1. `external_api`（重命名自当前的 `external`）
   - 使用带 URL 模板和 headers 的现有外部 API 模式。
   - 仅飞行器数据。
   - 无接收器/统计元数据。

2. `tar1090`
   - 仅由基础 URL 配置。
   - 必须读取并校验所有三个文件：
     - `aircraft.json`
     - `receiver.json`
     - `stats.json`
   - 飞行器摄入流程与当前本地 JSON 路径流相同。
   - 接收器/统计通过新 API 暴露。

3. `readsb_api`
   - 由飞行器端点的精确 URL 配置（示例：`http://192.168.1.60:30152/?all`）。
   - 仅飞行器数据。
   - 无接收器/统计元数据。

4. `readsb_file`
   - 除 `source_type` 外不需要每数据源配置。
   - 自动检测并从标准 readsb 运行时目录读取本地文件。
   - 必须消费：
     - `aircraft.json`
     - `receiver.json`
     - `stats.json`
   - 元数据暴露行为与 tar1090 三文件模式相同。

---

## 提议的配置模式

### 清晰策略（无遗留支持）

- 从代码和文档中移除遗留 ADS-B 键和别名。
- 仅接受新的显式 `source_type` 值和模式特定字段。
- 通过严格模式校验，对 `[adsb]` 中未知/遗留键的启动失败。
- 更新 `configs/config.toml.example` 仅展示新模式。

### 提议的 ADS-B 配置（目标）

```toml
[adsb]
# Required: external_api | tar1090 | readsb_api | readsb_file
source_type = "tar1090"

# Common polling settings
fetch_interval_seconds = 1
signal_lost_timeout_seconds = 60

# Used only for external_api
external_source_url = "https://.../lat/%f/lon/%f/dist/%.0f/"
api_host = "adsbexchange-com1.p.rapidapi.com"
api_key = ""
search_radius_nm = 50

# Used only for tar1090
# Base URL ending with /data/ recommended, but code should normalize with/without trailing slash
tar1090_base_url = "http://192.168.1.60/tar1090/data/"

# Used only for readsb_api
readsb_api_url = "http://192.168.1.60:30152/?all"

# Optional: custom readsb filesystem paths for readsb_file mode
# If empty, auto-detect from defaults.
readsb_data_dir = ""
```

### 数据源类型严格性

- 接受的值仅为：
  - `external_api`
  - `tar1090`
  - `readsb_api`
  - `readsb_file`

任何其他值都会校验失败。

---

## 自动检测：`readsb_file`

对于 `readsb_file`，启动按顺序探测目录（首个有效的胜出）：

1. `adsb.readsb_data_dir`（如果显式设置）
2. `/run/readsb`
3. `/var/run/readsb`
4. `/run/dump1090-fa`
5. `/run/dump1090-mutability`

候选目录的校验标准：

- 文件存在且可读：
  - `aircraft.json`
  - `receiver.json`
  - `stats.json`
- `aircraft.json` 解析为当前 `RawAircraftData` 模型。
- `receiver.json` 和 `stats.json` 解析为通用 JSON 对象（最初不要求严格模式）。

如果未找到有效目录，进程启动失败。

---

## 数据访问设计

## 1) 飞行器摄入抽象

引入数据源读取抽象（名称暂定）：

- `AircraftProvider`（返回解析后的飞行器载荷）
- `SourceMetaProvider`（返回可用的接收器/统计载荷）

具体实现：

- `ExternalAPIProvider`（`external_api`）
- `Tar1090Provider`（`tar1090`）
- `ReadsbAPIProvider`（`readsb_api`）
- `ReadsbFileProvider`（`readsb_file`）

`adsb.Client` 变为与数据源无关，并委托给所选 provider。

## 2) 元数据捕获与 API 暴露

元数据模型（暂定）：

```json
{
  "source_type": "tar1090",
  "source_label": "Tar1090",
  "receiver": { "...": "raw receiver.json" },
  "stats": { "...": "raw stats.json" },
  "updated_at": "2026-02-18T22:00:00Z",
  "available": true,
  "errors": []
}
```

服务行为：

- 为提供元数据的文件/API 数据源缓存最新成功的 receiver/stats。
- 对于无元数据的模式（`external_api`、`readsb_api`），如果飞行器流健康则返回 `receiver=null`、`stats=null` 和 `available=true`。

---

## 新 API 端点

为 Settings 侧边栏显示新增端点：

- `GET /api/v1/adsb/source`

响应（结构）：

```json
{
  "source_type": "tar1090",
  "mode": "tar1090",
  "status": "ok",
  "aircraft": {
    "available": true,
    "last_success_at": "2026-02-18T22:00:00Z",
    "last_error": ""
  },
  "receiver": {
    "available": true,
    "data": {}
  },
  "stats": {
    "available": true,
    "data": {}
  }
}
```

注意：

- 在 `external_api` 和 `readsb_api` 中，receiver/stats 可用性为 `false`，`data=null`。
- 端点为只读，不会改变现有飞行器 API。

---

## 启动校验与快速失败规则

如果配置的数据源不可访问或无效，系统必须在启动时终止。

每种模式的校验：

1. `external_api`
   - 校验所需配置字段。
   - 执行带超时的启动探测请求。
   - 要求 HTTP 200 且 JSON 解析成功。

2. `tar1090`
   - 校验 `tar1090_base_url`。
   - 探测所有必需文件（`aircraft.json`、`receiver.json`、`stats.json`）。
   - 全部要求 HTTP 200 且 JSON 解析成功。

3. `readsb_api`
   - 校验 `readsb_api_url`。
   - 探测 URL；要求 HTTP 200 且飞行器解析成功。

4. `readsb_file`
   - 自动检测目录。
   - 要求所有三个文件都存在/可读且可解析。

失败行为:

- 从配置/数据源校验路径返回明确错误。
- `main.go` 在服务启动前以非零代码退出。

---

## 前端计划（Settings 侧边栏）

位置：

- 在左侧 Settings 面板的当前 Debug 区块上方添加新区块。
- 建议标题：`ADS-B Source`。

显示字段（最小集）：

- 数据源类型/模式
- 飞行器订阅状态
- 接收器元数据状态/值预览（如可用）
- 统计元数据状态/值预览（如可用）
- 最后更新时间戳
- 最后错误文本（如有）

前端任务：

- 在 `www/app.js` 添加数据源元数据的存储状态。
- 按间隔（例如 5s）轮询 `GET /api/v1/adsb/source`，或附加在现有定期刷新中。
- 在 `www/index.html` 中直接在 Debug Settings 上方渲染该区块。

---

## 后端变更映射（计划文件）

- `internal/config/config.go`
  - 扩展 ADS-B 枚举 + 严格校验。
  - 添加模式特定的必需字段逻辑。

- `configs/config.toml.example`
  - 用新的 4 模式模式和注释替换旧的 ADS-B 区段。

- `internal/adsb/client.go`
  - 重构为基于 provider 的拉取。

- `internal/adsb/*`（可能新增文件）
  - 添加 `external_api`、`tar1090`、`readsb_api`、`readsb_file` 的 provider 实现。
  - 添加启动探测/校验辅助函数。
  - 添加元数据缓存结构体。

- `internal/adsb/service.go`
  - 为 API 处理器暴露数据源元数据访问器。

- `internal/api/handlers.go`
  - 添加 `GetADSBSourceStatus` 处理器。

- `internal/api/routes.go`
  - 注册 `GET /api/v1/adsb/source`。

- `www/app.js`
  - 拉取/存储 ADS-B 数据源元数据。

- `www/index.html`
  - 在 Debug 上方新增 Settings 区块。

- `docs/api_spec.md`
  - 记录新端点。

---

## 实施阶段

### 阶段 A - 配置 + 校验基础

1. 扩展配置模式和枚举处理。
2. 从结构体/校验中移除旧的 ADS-B 配置字段。
3. 添加严格的模式特定配置校验。

### 阶段 B - 数据源 Provider + 启动探测

1. 引入 provider 抽象。
2. 实现 4 个数据源 provider。
3. 为所选模式添加快速失败启动探测。

### 阶段 C - 数据源元数据 API

1. 为 tar1090/readsb_file 捕获 receiver/stats。
2. 添加 ADS-B 服务访问器。
3. 添加 `/api/v1/adsb/source` 路由/处理器。

### 阶段 D - 前端 Settings 面板

1. 在 Debug 上方添加 ADS-B Source 区块。
2. 将 UI 绑定到新端点数据。
3. 干净地处理无元数据模式。

### 阶段 E - 文档 + 手动校验

1. 更新配置示例和 API 文档。
2. 使用 `./build_windows.ps1` 构建。
3. 跨所有 4 种模式手动验证。

---

## 手动测试矩阵

1. `external_api` 配置有效 -> 应用启动；source 端点显示飞行器可用，receiver/stats 不可用。
2. `external_api` 无效的 key/url -> 启动失败。
3. `tar1090` 所有文件存在 -> 应用启动；source 端点返回 receiver/stats 载荷。
4. `tar1090` 缺少一个文件/404 -> 启动失败。
5. `readsb_api` URL 有效 -> 应用启动；仅飞行器可用。
6. `readsb_api` 404/无效 JSON -> 启动失败。
7. `readsb_file` 存在 `/run/readsb` -> 自动检测工作，元数据可用。
8. `readsb_file` 没有候选目录 -> 启动失败。

---

## 风险 / 备注

- 不同部署中 `stats.json` 和 `receiver.json` 的模式差异：最初存储并暴露原始 JSON。
- Windows 开发环境可能没有 `/run/readsb`；按设计 `readsb_file` 校验仍以 Linux 为目标。

---

## 审批关卡

在您审阅/批准此计划后，实施将按上述阶段顺序进行，最少不相关的变更。
