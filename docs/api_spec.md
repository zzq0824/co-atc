# Co-ATC API 规范

本文档详细介绍 Co-ATC API 端点、请求参数和响应格式。

## 飞行器数据端点

### GET /api/v1/aircraft

获取当前所有被追踪飞行器的列表。

**响应格式:**
```json
{
  "timestamp": "2025-05-19T01:02:03.456Z",
  "count": 2,
  "counts": {
    "ground_active": 5,
    "ground_total": 12,
    "air_active": 15,
    "air_total": 20
  },
  "aircraft": [
    {
      "hex": "a1b2c3",
      "flight": "SWA1234",
      "airline": "Southwest Airlines",
      "status": "active",
      "lat": 43.7,
      "lon": -79.5,
      "altitude": 35000,
      "heading": 90,
      "speed_gs": 450,
      "speed_true": 496,
      "vert_rate": -64,
      "category": "A5",
      "last_seen": "2025-05-19T01:02:03.456Z",
      "on_ground": false,
      "date_landed": null,
      "date_tookoff": "2025-05-19T00:45:12.123Z",
      "distance": 30.8,
      "is_simulated": false,
      "phase_data": {
        "current": {
          "phase": "CRZ",
          "changed_at": "2025-05-19T01:00:00.000Z"
        }
      },
      "clearances": [
        {
          "id": 1,
          "type": "takeoff",
          "text": "Cleared for takeoff runway 24R",
          "runway": "24R",
          "issued_at": "2025-05-19T01:00:00.000Z",
          "status": "active",
          "age": "2m"
        }
      ],
      "adsb": {
        "hex": "a1b2c3",
        "type": "adsb_icao",
        "flight": "SWA1234",
        "lat": 43.7,
        "lon": -79.5,
        "alt_baro": 35000,
        "alt_geom": 35250,
        "gs": 450,
        "ias": 292,
        "tas": 496,
        "mach": 0.852,
        "wd": 305,
        "ws": 89,
        "oat": -49,
        "tat": -17,
        "track": 90,
        "track_rate": 0,
        "roll": 0,
        "mag_heading": 86.48,
        "true_heading": 76.33,
        "baro_rate": -64,
        "geom_rate": 0,
        "squawk": "3151",
        "category": "A5",
        "nav_qnh": 1013.6,
        "nav_altitude_mcp": 35008,
        "nav_altitude_fms": 35008,
        "nav_heading": 85.08,
        "nic": 8,
        "rc": 186,
        "seen_pos": 6.431,
        "r_dst": 30.769,
        "r_dir": 141,
        "version": 2,
        "nic_baro": 1,
        "nac_p": 9,
        "nac_v": 1,
        "sil": 3,
        "sil_type": "perhour",
        "gva": 2,
        "sda": 2,
        "alert": 0,
        "spi": 0,
        "messages": 514,
        "seen": 5.9,
        "rssi": -18.6
      },
      "future": [
        {
          "lat": 43.72567,
          "lon": -79.658339,
          "altitude": 3400,
          "speed_gs": 208.3,
          "speed_true": 220.5,
          "heading": 327.15,
          "mag_heading": 325.8,
          "vertical_speed": 128,
          "timestamp": "2025-05-19T03:54:52-04:00",
          "distance": 5.4
        }
      ]
    }
  ]
}
```

**增强响应结构:**
- `counts`: 按地面/空中和活跃/总数划分的飞行器详细计数
- `distance`: 距离站点的距离(海里)
- `is_simulated`: boolean,指示飞行器是否为模拟飞行器
- `phase_data`: 当前飞行阶段信息
- `clearances`: 最近向飞行器发出的 ATC 许可
- `future`: 未来轨迹预测(最多 5 个位置)

响应包含按状态划分的飞行器详细计数:
- `counts.ground_active`: 当前正在传输数据的地面飞行器数量
- `counts.ground_total`: 被追踪的地面飞行器总数
- `counts.air_active`: 当前正在传输数据的空中飞行器数量
- `counts.air_total`: 被追踪的空中飞行器总数

每个飞行器的 `status` 字段指示其当前状态:
- `active`: 飞行器当前正在传输 ADS-B 数据
- `stale`: 飞行器最近未传输数据,但仍在历史窗口内
- `signal_lost`: 飞行器已从 ADS-B 覆盖范围中消失,但仍被追踪

**查询参数:**
- `min_altitude` (可选): 最小高度(英尺)
- `max_altitude` (可选): 最大高度(英尺)
- `status` (可选): 要包含的状态列表,以逗号分隔(active、stale、signal_lost)
- `callsign` (可选): 按呼号过滤(部分匹配)
- `last_seen_minutes` (可选): 仅包含在最近 N 分钟内被发现的飞行器
- `took_off_after` (可选): 仅包含在该时间之后起飞的飞行器(RFC3339 格式)
- `took_off_before` (可选): 仅包含在该时间之前起飞的飞行器(RFC3339 格式)
- `landed_after` (可选): 仅包含在该时间之后着陆的飞行器(RFC3339 格式)
- `landed_before` (可选): 仅包含在该时间之前着陆的飞行器(RFC3339 格式)
- `distance_nm` (可选): 仅包含距离参考点不超过该距离(海里)的飞行器
- `ref_lat` 和 `ref_lon` (可选): 用于距离过滤的参考坐标
- `ref_hex` (可选): 用于距离过滤的参考飞行器 hex 代码
- `ref_flight` (可选): 用于距离过滤的参考航班号
- `exclude_other_airports_grounded` (可选): 排除机场范围之外的地面飞行器(1 = true,0 = false)
- `simple` (可选): 返回仅包含必要字段的轻量级响应(1 = true,0 = false)

**简单响应格式 (当 `simple=1` 时):**
```json
{
  "timestamp": "2025-05-19T01:02:03.456Z",
  "count": 2,
  "aircraft": [
    {
      "hex": "a1b2c3",
      "callsign": "SWA1234",
      "registration": "N12345",
      "aircraft_type": "B738",
      "manufacturer": "Boeing",
      "registered_owners": "Southwest Airlines Co",
      "airline": "Southwest Airlines",
      "category": "A3",
      "lat": 43.7,
      "lon": -79.5,
      "alt_baro": 35000,
      "gs": 450,
      "track": 90,
      "vertical_rate": -64,
      "squawk": "3151",
      "distance": 30.8,
      "phase": "CRZ",
      "status": "active"
    }
  ]
}
```

简单响应不包含: 历史记录、未来预测、阶段历史、许可、航空公司信息、原始 ADSB 数据和详细计数。

### GET /api/v1/aircraft/{hex}

通过 ICAO hex 代码获取特定飞行器的数据。

**响应格式:**
与 `/aircraft` 端点中的单个飞行器对象相同。

### GET /api/v1/aircraft/{hex}/tracks

获取特定飞行器的位置历史和未来预测。

**数值精度策略 (适用于 `history`、`future` 和 `hindcast`):**
- GPS 坐标 (`lat`、`lon`): 6 位小数
- 运动/姿态字段 (`altitude`、`speed_gs`、`speed_true`、`track`、`true_heading`、`mag_heading`、`vertical_speed`): 整数
- `vertical_speed` 在可用时由存储数据填充,缺失时由相邻高度/时间点回退推导

**查询参数:**
- `limit` (可选): 返回的历史位置最大数量(默认: 1000,范围: 100-3600)

**响应格式:**
```json
{
  "hex": "a1b2c3",
  "flight": "SWA1234",
  "distance": 30.8,
  "history": [
    {
      "id": 12345,
      "lat": 43.71567,
      "lon": -79.668339,
      "altitude": 3300,
      "speed_gs": 208.3,
      "speed_true": 220.5,
      "heading": 327.15,
      "mag_heading": 325.8,
      "vertical_speed": 128,
      "timestamp": "2025-05-19T03:53:52-04:00",
      "distance": 5.2
    }
  ],
  "future": [
    {
      "lat": 43.72567,
      "lon": -79.658339,
      "altitude": 3400,
      "speed_gs": 208.3,
      "speed_true": 220.5,
      "heading": 327.15,
      "mag_heading": 325.8,
      "vertical_speed": 128,
      "timestamp": "2025-05-19T03:54:52-04:00",
      "distance": 5.4
    }
  ]
}
```

## 健康和状态端点

### GET /api/v1/health

返回服务器的健康状态。

**响应格式:**
```json
{
  "status": "active",
  "last_fetch": "2025-05-19T01:02:03.456Z",
  "aircraft_count": 25
}
```

### GET /api/v1/config

返回公开的配置设置。

**响应格式:**
```json
{
  "adsb": {
    "fetch_interval_seconds": 1
  },
  "storage": {
    "sqlite_base_path": "data/",
    "max_positions_in_api": 60
  },
  "frequencies": {
    "buffer_size_kb": 16,
    "stream_timeout_secs": 30,
    "reconnect_interval_secs": 5
  },
  "atc_chat": {
    "enabled": true
  }
}
```

### GET /api/v1/adsb/source

返回 ADS-B 源模式健康状况和源元数据,用于设置界面。

**响应格式:**
```json
{
  "source_type": "tar1090",
  "mode": "tar1090",
  "status": "ok",
  "aircraft": {
    "available": true,
    "last_success_at": "2026-02-18T22:00:00Z",
    "last_error": "",
    "data": {
      "messages": 123456,
      "aircraft_count": 67
    }
  },
  "receiver": {
    "available": true,
    "last_success_at": "2026-02-18T22:00:00Z",
    "last_error": "",
    "data": {}
  },
  "stats": {
    "available": true,
    "last_success_at": "2026-02-18T22:00:00Z",
    "last_error": "",
    "data": {}
  },
  "updated_at": "2026-02-18T22:00:00Z"
}
```

**模式行为:**
- `external-rapidapi`: `receiver` 和 `stats` 不可用 (`available=false`,`data=null`)
- `external-opensky`: `receiver` 和 `stats` 不可用 (`available=false`,`data=null`)
- `readsb-api`: `receiver` 和 `stats` 不可用 (`available=false`,`data=null`)
- `tar1090` 和 `readsb-file`: `receiver` 和 `stats` 包含来自源文件的原始 JSON 负载

### GET /api/v1/station

返回站点配置的位置和气象数据。

**响应格式:**
```json
{
  "latitude": 43.6777,
  "longitude": -79.6248,
  "elevation_feet": 569,
  "airport_code": "CYYZ",
  "fetch_metar": true,
  "fetch_taf": true,
  "fetch_notams": true,
  "metar": {
    "note": "Free from https://www.aviationweather.gov/dataserver",
    "source": "Internal",
    "trend": [
      {
        "metar": "CYYZ 210600Z 07007KT 15SM FEW220 BKN260 09/03 A2994 RMK CC2CI4 SLP144",
        "ux": 29130120,
        "type": "V",
        "txt": [
          "Wind 070° 7kt. Visibility 15sm. Clouds few 22000ft, broken 26000ft. Temperature 9°C, dew point 3°C. Altimeter 29.94inHg."
        ],
        "rmk": "CC2CI4 SLP144",
        "wind": {
          "dir": "070",
          "speedMPS": 4,
          "speed": 7,
          "measure": "KT"
        },
        "decoded": {
          "wind_direction": "070",
          "wind_speed": "7",
          "wind_unit": "KT",
          "visibility": "15SM",
          "temperature": "9",
          "dew_point": "3",
          "altimeter": "29.94"
        }
      }
    ]
  },
  "taf": {
    "raw": "CYYZ 210541Z 2106/2212 07008KT P6SM FEW220 SCT260 TX15/2112Z TN07/2110Z...",
    "decoded": []
  },
  "notams": []
}
```

### POST /api/v1/station

设置或清除站点坐标覆盖。

**请求体:**
```json
{
  "latitude": 43.6777,
  "longitude": -79.6248
}
```

**响应格式:**
```json
{
  "success": true,
  "message": "Station override coordinates set successfully",
  "latitude": 43.6777,
  "longitude": -79.6248
}
```

### GET /api/v1/wx

返回缓存的气象数据(METAR、TAF、NOTAM)。

**响应格式:**
```json
{
  "timestamp": "2025-05-19T01:02:03.456Z",
  "airport_code": "CYYZ",
  "metar": {
    "raw": "CYYZ 210600Z 07007KT 15SM FEW220 BKN260 09/03 A2994 RMK CC2CI4 SLP144",
    "decoded": {
      "wind_direction": "070",
      "wind_speed": "7",
      "wind_unit": "KT",
      "visibility": "15SM",
      "temperature": "9",
      "dew_point": "3",
      "altimeter": "29.94"
    }
  },
  "taf": {
    "raw": "CYYZ 210541Z 2106/2212 07008KT P6SM FEW220 SCT260...",
    "decoded": []
  },
  "notams": []
}
```

## 频率数据端点

### GET /api/v1/frequencies

获取所有被监听的 ATC 频率列表。

**响应格式:**
```json
{
  "timestamp": "2025-05-19T01:02:03.456Z",
  "count": 2,
  "frequencies": [
    {
      "id": "cyyz_dep",
      "airport": "CYYZ",
      "name": "Toronto Departures",
      "frequency_mhz": 127.575,
      "url": "https://s1-bos.liveatc.net/cyyz8",
      "status": "active",
      "bitrate": 128,
      "format": "mp3",
      "stream_url": "http://127.0.0.1:8080/api/v1/stream/cyyz_dep",
      "last_active": "2025-05-19T01:02:03.456Z",
      "order": 1
    }
  ]
}
```

### GET /api/v1/frequencies/{id}

通过 ID 获取特定频率的数据。

### GET /api/v1/stream/{id}

为特定频率提供音频流。

**响应头:**
```
Content-Type: audio/mpeg
Transfer-Encoding: chunked
Cache-Control: no-cache, no-store
X-Bitrate: 128
```

## WebSocket 端点

### GET /api/v1/ws

用于实时飞行器更新和转写的 WebSocket 端点。

**消息类型:**
- `aircraft_added`: 检测到新飞行器
- `aircraft_update`: 飞行器数据已更新
- `aircraft_predicted_state`: 用于客户端流畅运动的插值/预测飞行器状态
- `aircraft_removed`: 飞行器不再被追踪
- `aircraft_bulk_request`: 客户端请求批量飞行器数据
- `aircraft_bulk_response`: 服务器发送批量飞行器数据
- `filter_update`: 客户端更新过滤偏好
- `transcription`: 实时转写更新
- `phase_change`: 飞行器阶段变化
- `clearance_issued`: 已发出 ATC 许可
- `alert`: 系统告警

**客户端到服务器消息:**
```json
{
  "type": "aircraft_bulk_request",
  "data": {
    "filters": {
      "show_air": true,
      "show_ground": true,
      "phases": {"CRZ": true, "APP": true}
    }
  }
}
```

**服务器到客户端消息:**
```json
{
  "type": "aircraft_update",
  "data": {
    "aircraft": {
      "hex": "a1b2c3",
      "flight": "SWA1234",
      "status": "active"
    }
  }
}
```

**WebSocket 飞行器数值精度策略:**
- `aircraft_added` 完整负载 (`data.aircraft.adsb`):
  - `lat`、`lon` → 6 位小数
  - `alt_baro`、`alt_geom`、`gs`、`tas`、`ias`、`track`、`true_heading`、`mag_heading`、`baro_rate`、`geom_rate` → 整数
- `aircraft_update` 和 `aircraft_predicted_state` 增量 (`data.delta`):
  - `lat`、`lon` → 6 位小数
  - `alt_baro`、`alt_geom`、`gs`、`tas`、`ias`、`track`、`true_heading`、`mag_heading`、`baro_rate`、`geom_rate`、`vertical_speed`、`vertical_rate` → 整数

## ATC Chat 端点

### POST /api/v1/atc-chat/session

创建一个新的 ATC chat 会话。

**请求体:**
```json
{
  "instructions": "Custom AI instructions",
  "speed": 1.5
}
```

**响应格式:**
```json
{
  "session_id": "12345",
  "status": "created",
  "expires_at": "2025-05-19T02:02:03.456Z"
}
```

### DELETE /api/v1/atc-chat/session/{sessionId}

结束 ATC chat 会话。

**响应格式:**
```json
{
  "status": "success",
  "session_id": "12345",
  "message": "Session ended successfully"
}
```

### GET /api/v1/atc-chat/session/{sessionId}/status

获取 ATC chat 会话的状态。

**响应格式:**
```json
{
  "id": "12345",
  "active": true,
  "connected": true,
  "last_activity": "2025-05-19T01:02:03.456Z",
  "expires_at": "2025-05-19T02:02:03.456Z"
}
```

### POST /api/v1/atc-chat/session/{sessionId}/update-context

使用最新的空域数据更新会话上下文。

**响应格式:**
```json
{
  "status": "success",
  "message": "Session context updated with fresh airspace data"
}
```

### GET /api/v1/atc-chat/sessions

列出所有活跃的 ATC chat 会话。

**响应格式:**
```json
{
  "sessions": [
    {
      "id": "12345",
      "active": true,
      "connected": true,
      "last_activity": "2025-05-19T01:02:03.456Z",
      "expires_at": "2025-05-19T02:02:03.456Z"
    }
  ]
}
```

### GET /api/v1/atc-chat/airspace-status

获取 ATC chat 当前的空域状态。

**响应格式:**
```json
{
  "aircraft_count": 25,
  "active_count": 20,
  "frequencies_active": 3,
  "last_updated": "2025-05-19T01:02:03.456Z"
}
```

### GET /api/v1/atc-chat/ws/{sessionId}

用于 ATC chat 音频流的 WebSocket 端点。

**WebSocket 消息类型:**
- `connection_ready`: 客户端连接已建立
- `openai_ready`: OpenAI 连接已建立
- `connection_error`: 发生连接错误
- `session.update`: 会话上下文已更新
- `response.audio.delta`: 音频响应分块
- `response.audio.done`: 音频响应完成

## 模拟端点

### POST /api/v1/simulation/aircraft

创建一个模拟飞行器。

**请求体:**
```json
{
  "hex": "123456",
  "flight": "SIM001",
  "lat": 43.6777,
  "lon": -79.6248,
  "altitude": 5000,
  "heading": 90,
  "speed": 200,
  "vertical_rate": 0
}
```

**响应格式:**
```json
{
  "success": true,
  "message": "Simulated aircraft created successfully",
  "hex": "123456"
}
```

### PUT /api/v1/simulation/aircraft/{hex}/controls

更新飞行器的模拟控制参数。

**请求体:**
```json
{
  "heading": 180,
  "speed": 250,
  "vertical_rate": 500
}
```

**响应格式:**
```json
{
  "success": true,
  "message": "Simulation controls updated successfully"
}
```

### DELETE /api/v1/simulation/aircraft/{hex}

移除一个模拟飞行器。

**响应格式:**
```json
{
  "success": true,
  "message": "Simulated aircraft removed successfully"
}
```

### GET /api/v1/simulation/aircraft

列出所有模拟飞行器。

**响应格式:**
```json
{
  "timestamp": "2025-05-19T01:02:03.456Z",
  "count": 1,
  "aircraft": [
    {
      "hex": "123456",
      "flight": "SIM001",
      "lat": 43.6777,
      "lon": -79.6248,
      "altitude": 5000,
      "heading": 90,
      "speed": 200,
      "vertical_rate": 0,
      "is_simulated": true,
      "created_at": "2025-05-19T01:00:00.000Z"
    }
  ]
}
```

## 转写端点

### GET /api/v1/transcriptions

返回所有转写的分页列表。

**查询参数:**
- `limit` (可选): 返回的最大转写数量(默认: 100)
- `offset` (可选): 分页偏移量(默认: 0)

**响应格式:**
```json
{
  "timestamp": "2025-05-20T20:15:35Z",
  "count": 2,
  "transcriptions": [
    {
      "id": 123,
      "frequency_id": "cyyz_grd",
      "created_at": "2025-05-20T20:15:35Z",
      "content": "Delta 123, cleared to land runway 24R",
      "is_complete": true,
      "is_processed": true,
      "content_processed": "Clearance: Landing clearance issued",
      "speaker_type": "ATC",
      "callsign": ""
    }
  ]
}
```

### GET /api/v1/transcriptions/frequency/{id}

返回特定频率的转写。

### GET /api/v1/transcriptions/time-range

返回指定时间范围内的转写。

**查询参数:**
- `start_time` (必填): 开始时间(RFC3339 格式)
- `end_time` (可选): 结束时间(RFC3339 格式)
- `limit` (可选): 返回的最大转写数量(默认: 100)
- `offset` (可选): 分页偏移量(默认: 0)

### GET /api/v1/transcriptions/speaker/{type}

按说话人类型(ATC 或 PILOT)返回转写。

### GET /api/v1/transcriptions/callsign/{callsign}

返回特定飞行器呼号的转写。

## 错误响应

所有端点都返回适当的 HTTP 状态码:
- `200 OK`: 成功
- `400 Bad Request`: 请求参数无效
- `404 Not Found`: 资源未找到
- `500 Internal Server Error`: 服务器错误

错误响应包含一个带有错误详情的 JSON 对象:
```json
{
  "error": "Invalid parameter",
  "message": "Aircraft not found"
}
```
