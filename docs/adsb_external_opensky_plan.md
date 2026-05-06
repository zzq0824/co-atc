# ADS-B `external-opensky` 数据源计划（请先审阅）

## 目标

新增一种 ADS-B 数据源类型：使用 OpenSky REST 状态向量的 `external-opensky`，同时保留当前清晰的源架构。
此模式针对没有本地 ADS-B 接收器的用户，使用公共 OpenSky 网络数据。

这是一份**仅作计划**的文档（暂无代码变更）。

主要要求：
- 在可用时，优先使用 OpenSky 的 `on_ground` 信号，而非本地启发式地面检测。

---

## 范围

### 范围内
- 新的 `adsb.source_type = "external-opensky"`。
- 通过 OpenSky 公共 `/states/all` 端点获取飞行器。
- 将 OpenSky 状态向量数组格式转换为内部 `RawAircraftData` / `ADSBTarget`。
- 当存在 OpenSky `on_ground` 时将其作为权威值。
- 启动探测 + 与当前数据源校验行为一致的快速失败。
- 为新模式更新配置/文档。

### 范围外（MVP）
- 历史 OpenSky 端点（`/flights/*`、`/tracks`）。
- OpenSky `/states/own` 接收器特定端点。
- 接收器/统计元数据（OpenSky 不提供 tar1090 风格的 `receiver.json`/`stats.json`）。
- 跨 OpenSky + 其他数据源的多端点混合。

---

## OpenSky API 形态（关键细节）

端点基础：`https://opensky-network.org/api`

主要调用：
- `GET /states/all`（可选携带 bbox 查询参数 `lamin`、`lomin`、`lamax`、`lomax`）。

响应形态：
- 顶层对象包含：
  - `time`（unix 秒）
  - `states`（二维数组）
- 实时样本检查（瑞士 bbox）在未设置 `extended` 时返回带有 17 个字段（`0..16`）的默认行。

每个 `states[i]` 是一个索引数组：
- `0` `icao24`（hex）
- `1` `callsign`
- `5` `longitude`
- `6` `latitude`
- `7` `baro_altitude`（米）
- `8` `on_ground`（布尔值）
- `9` `velocity`（m/s）
- `10` `true_track`（度）
- `11` `vertical_rate`（m/s）
- `13` `geo_altitude`（米）
- `14` `squawk`
- `15` `spi`
- `16` `position_source`（0=ADS-B、1=ASTERIX、2=MLAT、3=FLARM）
- `17` `category`（**仅在** `extended=1` 时存在）

重要行为说明：
- 部分字段为 null。
- callsign 通常带有填充/尾部空白，应做 trim 处理。
- 匿名访问受速率/时间分辨率限制。
- 认证访问有不同限制；新账号必须使用 OAuth2 客户端凭据流程。

---

## 提议的配置新增项

向 `[adsb]` 添加 OpenSky 模式和设置：

```toml
[adsb]
source_type = "external-opensky"

# Existing shared fields reused for area selection
search_radius_nm = 50

# OpenSky configuration
opensky_base_url = "https://opensky-network.org/api"
opensky_token_url = "https://auth.opensky-network.org/auth/realms/opensky-network/protocol/openid-connect/token"

# Auth mode: anonymous | oauth2
opensky_auth_mode = "anonymous"
opensky_oauth2_credentials_path = "configs/opensky_credentials.json"

fetch_interval_seconds = 1
signal_lost_timeout_seconds = 60
```

凭据文件格式（`opensky_oauth2_credentials_path`）：

```json
{"clientId":"your-api-client-id","clientSecret":"your-api-client-secret"}
```

校验规则（计划中）：
- `source_type` 接受新值 `external-opensky`。
- 此模式下 `opensky_base_url` 必填。
- `opensky_auth_mode` 必须是 `anonymous|oauth2` 之一。
- `oauth2` 需要 `opensky_oauth2_credentials_path`。
- 当 `opensky_auth_mode=oauth2` 时需要 `opensky_token_url`（默认见上）。
- 需要 `search_radius_nm > 0`（用于根据站点坐标构建 bbox）。

---

## 数据映射计划（OpenSky -> ADSBTarget）

创建一个 OpenSky 解析器，安全地处理 null/缺失索引并转换单位：

- `hex` <= `state[0]`
- `flight` <= 已 trim 的 `state[1]`
- `lon` <= `state[5]`
- `lat` <= `state[6]`
- `alt_baro` <= 米转英尺(`state[7]`)
- `alt_geom` <= 米转英尺(`state[13]`)
- `gs` <= m/s 转节(`state[9]`)
- `track` <= `state[10]`
- `baro_rate` <= m/s 转 ft/min(`state[11]`)
- `squawk` <= `state[14]`
- `spi` <= bool->int 或与现有模型一致的专用 bool 处理
- `category` <= 当 `extended=1` 时为字符串化的 `state[17]`，否则为空/未知
- `position_source` <= 来自 `state[16]` 的可选诊断字段（MVP 逻辑非必须）
- `source_type` <= `external-opensky`

原始信封映射：
- `RawAircraftData.Now` <= 响应 `time`（回退到当前 unix 时间）。
- `RawAircraftData.Messages` <= 0（OpenSky 响应不提供消息计数）。

同时将原始 OpenSky 地面标志携带到目标级元数据中，使服务可以将其作为权威值使用。

---

## `on_ground` 优先级规则（关键）

当前服务通过本地启发式（`IsFlying(...)`）派生地面/空中状态，并具有无源数据回退逻辑。

每架飞行器的计划优先级：
1. 如果是 OpenSky 数据源且 OpenSky `on_ground` 存在：**直接使用**。
2. 否则：使用现有启发式路径（`IsFlying`、无可用数据保护、保留先前状态）。

实施说明：
- 用一个可选的源地面字段扩展 `ADSBTarget`（例如 `OnGroundReported *bool` 或等价物），以避免重载不相关的数值字段。
- 对所有非 OpenSky 数据源保持现有启发式代码不变。

---

## 请求构造计划

对于 OpenSky 请求：
- 将 URL 构建为 `opensky_base_url + /states/all`。
- 根据站点 lat/lon + `search_radius_nm` 添加 bbox 查询。
  - MVP 使用保守的大地近似：
    - `deltaLat = radiusNm / 60`
    - `deltaLon = radiusNm / (60 * cos(lat))`（注意高纬度保护）
- 对于 `anonymous`，不发送认证头。
- 对于 `oauth2`，使用 `grant_type=client_credentials`、`client_id`、`client_secret` 从 `opensky_token_url` 获取 token，然后发送 `Authorization: Bearer <token>`。
- 在内存中缓存 token 并主动刷新（或在首次 `401` 时刷新），因为 token 生命周期约为 30 分钟。

理由：
- 与全球 `/states/all` 相比，边界框可减少负载大小和 API 配额使用。

---

## 启动校验 / 快速失败

`external-opensky` 启动探测：
- 如果启用了 `oauth2`，先校验凭据文件加载 + token 获取。
- 使用配置的认证 + bbox 执行单次定时 `/states/all` 请求。
- 要求 HTTP 200 且可解析的 OpenSky 响应对象。
- 要求 `states` 存在（可以是空数组）。
- 失败时：返回明确的校验错误并中止启动（沿用现有致命启动模式）。

---

## API/前端影响

`GET /api/v1/adsb/source` 保持相同模式：
- `source_type = external-opensky`
- `aircraft.available` 反映拉取健康状况
- `receiver.available = false`
- `stats.available = false`

无需新增 UI 区块；现有 ADS-B 数据源面板应像现在一样显示模式/状态。

---

## 计划的代码触点

- `internal/config/config.go`
  - 向 `ADSBConfig` 添加 OpenSky 字段。
  - 更新校验 switch + 错误消息。

- `internal/adsb/source_status.go`
  - 添加 `SourceTypeExternalOpenSky` 常量。

- `internal/adsb/client.go`
  - 添加 `external-opensky` 拉取分支。
  - 添加 OpenSky 请求构造器、OAuth2 token 客户端（基于凭据文件）、解析器。

- `internal/adsb/models.go`
  - 添加源报告的地面状态可选字段。
  - 更新 source_type 注释枚举列表。

- `internal/adsb/service.go`
  - 在处理管道中应用 `on_ground` 优先级规则。

- `configs/config.toml.example`
  - 记录新数据源类型和 OpenSky 字段。

- `docs/api_spec.md`
  - 在相关位置更新 source_type 枚举。

---

## 风险与缓解

1. OAuth2 token 生命周期与凭据处理
- 缓解：从文件加载凭据，内存中 token 缓存，在过期/401 时刷新，无效凭据时给出清晰的启动错误。

2. 数组索引负载脆弱性
- 缓解：稳健的索引保护和空值安全的类型化提取辅助函数。

3. 速率限制 / 配额使用
- 缓解：始终查询有界区域；保持启动探测单次。

4. 单位转换错误
- 缓解：集中的转换辅助函数，并在调试模式下记录样本转换值。

---

## 校验矩阵（实施后）

1. 匿名模式
- 配置有效则启动；`/states/all` 解析；飞行器被摄入。

2. OAuth2 模式
- 凭据文件加载；token 获取成功；带认证的 `/states/all` 拉取工作。
- 无效的 `clientId/clientSecret` 干净地启动失败。
- 过期 token 路径会刷新并恢复（或带明确认证错误失败）。

3. 地面优先级
- 对于 `on_ground=true` 的 OpenSky 目标，即使本地启发式提示在飞，服务也存储 `OnGround=true`。
- 对于 `on_ground=false`，除非应用了无数据回退，服务存储为飞行中。
- 非 OpenSky 数据源不变。

4. API/UI 状态
- `/api/v1/adsb/source` 返回 `external-opensky` 模式且 receiver/stats 不可用。

---

## 推出步骤

1. 添加配置模式 + 校验。
2. 添加源常量 + 客户端拉取/解析器。
3. 添加 `on_ground` 源字段支持 + 服务优先级。
4. 更新配置/文档。
5. 构建 + 手动冒烟测试（`build_windows.ps1`、启动探测、UI 状态面板）。

如果此计划看起来不错，下一步是按上述顺序实施。
