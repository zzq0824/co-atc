#!/usr/bin/env bash
# 下载 co-atc 所需的参考数据文件
# 从项目根目录运行:./scripts/download_assets.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="${1:-$SCRIPT_DIR/../assets}"
ASSETS_DIR="$(cd "$ASSETS_DIR" 2>/dev/null && pwd || mkdir -p "$ASSETS_DIR" && cd "$ASSETS_DIR" && pwd)"

echo "正在将资源下载到:$ASSETS_DIR"

# 1. 来自 wiedehopf/tar1090-db 的 aircraft.csv(gzip 压缩)
echo -e "\n[1/6] 正在下载 aircraft.csv.gz……"
curl -fSL "https://github.com/wiedehopf/tar1090-db/raw/refs/heads/csv/aircraft.csv.gz" -o "$ASSETS_DIR/aircraft.csv.gz"
gunzip -f "$ASSETS_DIR/aircraft.csv.gz"
echo "  -> aircraft.csv 解压完成"

# 2. 来自 OpenFlights 的 airlines.dat
echo -e "\n[2/6] 正在下载 airlines.dat……"
curl -fSL "https://raw.githubusercontent.com/jpatokal/openflights/master/data/airlines.dat" -o "$ASSETS_DIR/airlines.dat"
echo "  -> airlines.dat 下载完成"

# 3. 来自 OurAirports 的 airports.csv
echo -e "\n[3/6] 正在下载 airports.csv……"
curl -fSL "https://davidmegginson.github.io/ourairports-data/airports.csv" -o "$ASSETS_DIR/airports.csv"
echo "  -> airports.csv 下载完成"

# 4. 来自 OurAirports 的 airport-frequencies.csv
echo -e "\n[4/6] 正在下载 airport-frequencies.csv……"
curl -fSL "https://davidmegginson.github.io/ourairports-data/airport-frequencies.csv" -o "$ASSETS_DIR/airport-frequencies.csv"
echo "  -> airport-frequencies.csv 下载完成"

# 5. 来自 OurAirports 的 runways.csv
echo -e "\n[5/6] 正在下载 runways.csv……"
curl -fSL "https://davidmegginson.github.io/ourairports-data/runways.csv" -o "$ASSETS_DIR/runways.csv"
echo "  -> runways.csv 下载完成"

# 6. 来自 OurAirports 的 navaids.csv
echo -e "\n[6/6] 正在下载 navaids.csv……"
curl -fSL "https://davidmegginson.github.io/ourairports-data/navaids.csv" -o "$ASSETS_DIR/navaids.csv"
echo "  -> navaids.csv 下载完成"

echo -e "\n所有资源下载成功!"
