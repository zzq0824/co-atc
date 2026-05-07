# Co-ATC Docker 部署

本指南提供使用 Docker 和 Docker Compose 部署 Co-ATC 的说明。但说真的，不要这样做，请原生运行。

## 概述

Co-ATC 是一个 AI 增强的飞行器监控系统，提供：

- 带交互式地图的实时飞行器追踪
- 来自多个 ATC 频率的音频流
- 无线电通信的 AI 转写（需要 OpenAI API）
- 用于 ATC 查询的 AI 语音助手
- 天气集成（METAR/TAF/NOTAMs）
- WebSocket 实时数据更新

## 快速开始

### 选项 1：自动安装（推荐）

1. **运行安装脚本：**
   ```bash
   cd docker
   ./start.sh
   ```

2. **该脚本将：**
   - 检查所需文件和依赖
   - 从示例模板创建配置文件
   - 构建并启动容器
   - 在浏览器中打开 web 界面

3. **配置您的设置：**
   - 当提示时，使用您的设置编辑 `config.toml`（参见下方"配置"部分）
   - 再次运行脚本以启动系统

### 选项 2：手动安装

1. **创建配置文件：**
   ```bash
   cd docker
   cp ../configs/config.toml.example config.toml
   ```

2. **编辑配置文件：**
   ```bash
   nano config.toml
   ```

3. **启动系统：**
   ```bash
   docker-compose up -d
   ```

4. **访问界面：**
   - 在浏览器中打开：`http://localhost:8000`

## 先决条件

- 已安装 Docker 和 Docker Compose
- ADS-B 数据源（tar1090、readsb、OpenSky 或外部 RapidAPI）
- OpenAI API key（可选，用于 AI 功能）
- 容器中已包含 FFmpeg 支持

## 配置

配置文件从 `/configs/config.toml.example` 创建。您必须为您的环境自定义以下设置：

### 必需设置

#### 1. ADS-B 数据源
配置您的飞行器数据源：

```toml
[adsb]
# Source types:
# - "tar1090"
# - "readsb-api"
# - "readsb-file"
# - "external-rapidapi"
# - "external-opensky"

# Example: tar1090
source_type = "tar1090"
tar1090_base_url = "http://your-tar1090-server:8080/tar1090/data/"

# Example: readsb API
# source_type = "readsb-api"
# readsb_api_url = "http://your-readsb-host:30152/?all"

# Example: external RapidAPI
# source_type = "external-rapidapi"
# external_source_url = "https://adsbexchange-com1.p.rapidapi.com/v2/lat/%f/lon/%f/dist/%.0f/"
# api_host = "adsbexchange-com1.p.rapidapi.com"
# api_key = "your-api-key-here"

# Example: external OpenSky (anonymous)
# source_type = "external-opensky"
# opensky_base_url = "https://opensky-network.org/api"
# opensky_auth_mode = "anonymous"

# Example: external OpenSky (OAuth2 client credentials)
# source_type = "external-opensky"
# opensky_auth_mode = "oauth2"
# opensky_token_url = "https://auth.opensky-network.org/auth/realms/opensky-network/protocol/openid-connect/token"
# opensky_oauth2_credentials_path = "configs/opensky_credentials.json"
```

#### 2. 站点位置
设置您的监控站点坐标：

```toml
[station]
latitude = 43.6777      # Your latitude
longitude = -79.6248    # Your longitude
elevation_feet = 569    # Your elevation in feet
airport_code = "CYYZ"   # Your airport ICAO code
```

#### 3. 无线电频率
配置要监控的 ATC 频率：

```toml
[[frequencies.sources]]
id = "tower"
airport = "CYYZ"
name = "Tower"
frequency_mhz = 118.700
url = "https://your-audio-stream-url"
order = 1
transcribe_audio = true
```

### 可选设置

#### 4. OpenAI API 集成
使用您的 OpenAI API key 启用 AI 功能：

```toml
[transcription]
openai_api_key = "sk-your-key-here"

[atc_chat]
openai_api_key = "sk-your-key-here"
```

#### 5. 服务器配置
默认服务器设置可与 Docker 配合使用：

```toml
[server]
host = "0.0.0.0"        # Bind to all interfaces
port = 8080             # Internal port (mapped to 8000 externally)
```

### 配置注意事项

- 容器在内部端口 8080 上运行,映射到外部端口 8000
- 数据库文件存储在持久化的 `data` 卷中
- 提供了多伦多皮尔逊机场（CYYZ）的配置示例
- 音频流需要有效的 URL（LiveATC、本地 SRT 流等）
- AI 功能需要具有适当权限的有效 OpenAI API key



## 数据持久化

该设置包括以下持久卷：

- **`co-atc-data`** - SQLite 数据库和应用程序数据
- **`co-atc-logs`** - 应用程序日志

**文件挂载：**
- **`../assets`** - 静态数据文件（航空公司、机场、跑道）
- **`../prompts`** - AI 系统提示词
- **`../www`** - Web 界面文件（HTML、CSS、JS）
- **`./config.toml`** - 您的配置文件

数据在容器重启和更新后仍然保留。

## 部署命令

### 推荐：使用自动化脚本
```bash
cd docker
./start.sh
```

### 手动命令

**启动系统：**
```bash
cd docker
docker-compose up -d
```

**查看日志（实时）：**
```bash
docker-compose logs -f co-atc
```

**查看最近日志：**
```bash
docker-compose logs --tail 50 co-atc
```

**检查系统状态：**
```bash
docker-compose ps
curl -I http://localhost:8000
```

**重启系统：**
```bash
docker-compose restart co-atc
```

**停止系统：**
```bash
docker-compose down
```

**更新并重新构建：**
```bash
docker-compose down
docker-compose build --no-cache
docker-compose up -d
```

**清理所有内容（⚠️ 销毁数据）：**
```bash
docker-compose down -v
docker system prune -a
```

### 快速状态检查
```bash
# Check if everything is running
docker-compose ps
docker-compose logs co-atc | grep "Starting HTTP server"
curl -I http://localhost:8000
```

## 🌐 网络配置

系统暴露多个端口：
- **8000** - 主 web 界面
- **8001-8004** - 附加界面

### 反向代理后

Nginx 配置示例：
```nginx
server {
    listen 80;
    server_name your-domain.com;
    
    location / {
        proxy_pass http://localhost:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # WebSocket support
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

## 故障排查

### 常见问题：「Invalid configuration: frequency #1: URL is required」
此错误表明配置文件未正确加载或包含无效的频率配置。

**解决方案：**
```bash
# Check if config file exists
ls -la docker/config.toml

# Check docker-compose.yml volume mount (should be exactly this):
grep -A 1 "Configuration" docker/docker-compose.yml
# Should show: - ./config.toml:/app/configs/config.toml:ro

# Verify frequency configuration in config.toml
grep -A 10 "frequencies.sources" docker/config.toml
```

### 常见问题：容器无法启动
```bash
# Check Docker is running
docker info

# Check logs for specific error
docker-compose logs co-atc

# Verify configuration syntax
docker-compose config

# Force rebuild if needed
docker-compose build --no-cache co-atc
```

### 常见问题：Web 界面无法访问
应用程序在容器内的 8080 端口上运行，映射到外部端口 8000。

**解决方案：**
```bash
# Check if container is running
docker-compose ps

# Test internal port first
docker exec co-atc wget -qO- http://localhost:8080 | head -10

# Test external port mapping
curl -I http://localhost:8000
```

### 常见问题：无飞行器数据
1. 检查 `config.toml` 中的 ADS-B 数据源 URL
2. 验证网络连通性：`ping your-tar1090-server`
3. 检查 tar1090 是否正在运行且可访问
4. 在日志中查找 ADS-B 错误：`docker-compose logs co-atc | grep adsb`

### 常见问题：缺少 assets/prompts 错误
1. 确保目录存在：`ls -la ../assets/ ../prompts/`
2. 检查所需文件存在：
   ```bash
   ls -la ../assets/*.json
   ls -la ../prompts/*.txt
   ```
3. 验证卷挂载：`docker exec co-atc ls -la assets/ prompts/`

### 常见问题：音频/频率问题
1. 验证 FFmpeg 工作正常：`docker exec co-atc ffmpeg -version`
2. 检查容器中的音频流 URL 是否可访问：
   ```bash
   docker exec co-atc wget --spider https://s1-bos.liveatc.net/cyyz7
   ```
3. 检查 `config.toml` 中的频率配置
4. 检查频率错误：`docker-compose logs co-atc | grep freq`

### 常见问题：AI 功能无法使用
1. 验证 `config.toml` 中已设置 OpenAI API key
2. 检查 API key 权限和账单
3. 监控日志中的 API 错误：`docker-compose logs co-atc | grep openai`

### 常见问题：健康检查失败
```bash
# Check container health
docker-compose ps

# Manual health check
docker exec co-atc wget --no-verbose --tries=1 --spider http://localhost:8080/

# Check if port is accessible
curl -I http://localhost:8000
```

## 📊 监控

### 资源使用：
```bash
docker stats co-atc
```

### 数据库大小：
```bash
docker exec co-atc ls -lh data/
```

### 应用程序状态：
```bash
curl http://localhost:8000/
```

## 🔒 安全注意事项

**⚠️ 重要安全警告：**

此应用程序**没有身份验证**，**绝对不应**暴露到互联网！

### 安全措施：
- 仅绑定到 localhost (`127.0.0.1:8000`)
- 使用带身份验证的反向代理
- 保持容器更新
- 监控日志中的可疑活动

### 生产使用：
1. 设置适当的身份验证
2. 使用 HTTPS/TLS 加密
3. 实施速率限制
4. 定期安全更新
5. 网络分段

## 🐛 常见问题

### 「Permission denied」错误：
```bash
# Fix volume permissions
docker-compose down
sudo chown -R 1000:1000 /var/lib/docker/volumes/docker_co-atc-data/
docker-compose up -d
```

### 「Config file not found」：
```bash
# Ensure config file exists
ls -la docker/config.toml.local
# Check volume mount in docker-compose.yml
```

### 高 CPU 使用率：
- 减小配置中的 `fetch_interval_seconds`
- 禁用不必要的频率
- 检查日志中的无限循环

### 内存问题：
- 增加容器内存限制
- 检查日志中的内存泄漏
- 定期重启容器

## 🚀 性能调优

### 资源限制：
```yaml
deploy:
  resources:
    limits:
      memory: 1G        # Increase if needed
      cpus: '2.0'       # Increase for better performance
```

### 数据库优化：
- 定期数据库清理
- 监控数据库大小
- 对于大流量部署考虑外部数据库

## 📚 附加资源

- [Main Project README](../README.md)
- [API Documentation](../docs/api_spec.md)
- [Technical Documentation](../docs/technical_docs.md)
- [Project Progress](../docs/project_progress.md)

## 支持

这是一个开发/业余项目。如有问题：
1. 先检查日志
2. 验证您的配置
3. 查阅本 README
4. 检查主项目文档

## 附加资源

- [Main Project README](../README.md)
- [API Documentation](../docs/api_spec.md)
- [Technical Documentation](../docs/technical_docs.md)
- [Project Progress](../docs/project_progress.md)
