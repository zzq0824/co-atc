# 下载 co-atc 所需的参考数据文件
# 从项目根目录运行:.\scripts\download_assets.ps1

param(
    [string]$AssetsDir = (Join-Path (Join-Path $PSScriptRoot "..") "assets")
)

$ErrorActionPreference = "Stop"

# 转换为绝对路径
$AssetsDir = (Resolve-Path $AssetsDir).Path
Write-Host "正在将资源下载到:$AssetsDir" -ForegroundColor Cyan

# 辅助函数:先写入临时文件再移动到目标位置
# 这样可以避免应用占用目标文件时出现 "file in use" 错误
function Get-AssetFile {
    param([string]$Uri, [string]$Dest)
    $tmp = "$Dest.tmp"
    Invoke-WebRequest -Uri $Uri -OutFile $tmp
    Copy-Item -Path $tmp -Destination $Dest -Force
    Remove-Item $tmp -ErrorAction SilentlyContinue
}

# 1. 来自 wiedehopf/tar1090-db 的 aircraft.csv(gzip 压缩)
Write-Host "`n[1/6] 正在下载 aircraft.csv.gz……" -ForegroundColor Yellow
$aircraftGz = Join-Path $AssetsDir "aircraft.csv.gz"
$aircraftCsv = Join-Path $AssetsDir "aircraft.csv"
$aircraftTmp = "$aircraftCsv.tmp"
Invoke-WebRequest -Uri "https://github.com/wiedehopf/tar1090-db/raw/refs/heads/csv/aircraft.csv.gz" -OutFile $aircraftGz
# 解压到临时文件后再移动到目标位置
$inStream = [System.IO.File]::OpenRead($aircraftGz)
$output = [System.IO.File]::Create($aircraftTmp)
$gzip = New-Object System.IO.Compression.GZipStream($inStream, [System.IO.Compression.CompressionMode]::Decompress)
$gzip.CopyTo($output)
$gzip.Close()
$output.Close()
$inStream.Close()
Remove-Item $aircraftGz
Copy-Item -Path $aircraftTmp -Destination $aircraftCsv -Force
Remove-Item $aircraftTmp -ErrorAction SilentlyContinue
Write-Host "  -> aircraft.csv 解压完成" -ForegroundColor Green

# 2. 来自 OpenFlights 的 airlines.dat
Write-Host "`n[2/6] 正在下载 airlines.dat……" -ForegroundColor Yellow
Get-AssetFile "https://raw.githubusercontent.com/jpatokal/openflights/master/data/airlines.dat" (Join-Path $AssetsDir "airlines.dat")
Write-Host "  -> airlines.dat 下载完成" -ForegroundColor Green

# 3. 来自 OurAirports 的 airports.csv
Write-Host "`n[3/6] 正在下载 airports.csv……" -ForegroundColor Yellow
Get-AssetFile "https://davidmegginson.github.io/ourairports-data/airports.csv" (Join-Path $AssetsDir "airports.csv")
Write-Host "  -> airports.csv 下载完成" -ForegroundColor Green

# 4. 来自 OurAirports 的 airport-frequencies.csv
Write-Host "`n[4/6] 正在下载 airport-frequencies.csv……" -ForegroundColor Yellow
Get-AssetFile "https://davidmegginson.github.io/ourairports-data/airport-frequencies.csv" (Join-Path $AssetsDir "airport-frequencies.csv")
Write-Host "  -> airport-frequencies.csv 下载完成" -ForegroundColor Green

# 5. 来自 OurAirports 的 runways.csv
Write-Host "`n[5/6] 正在下载 runways.csv……" -ForegroundColor Yellow
Get-AssetFile "https://davidmegginson.github.io/ourairports-data/runways.csv" (Join-Path $AssetsDir "runways.csv")
Write-Host "  -> runways.csv 下载完成" -ForegroundColor Green

# 6. 来自 OurAirports 的 navaids.csv
Write-Host "`n[6/6] 正在下载 navaids.csv……" -ForegroundColor Yellow
Get-AssetFile "https://davidmegginson.github.io/ourairports-data/navaids.csv" (Join-Path $AssetsDir "navaids.csv")
Write-Host "  -> navaids.csv 下载完成" -ForegroundColor Green

Write-Host "`n所有资源下载成功!" -ForegroundColor Cyan
