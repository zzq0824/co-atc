# Co-ATC 服务器构建脚本
# 该脚本会在 bin 目录下生成 Windows AMD64 可执行文件

# 如果 bin 目录不存在则创建
if (-not (Test-Path -Path "bin")) {
    New-Item -ItemType Directory -Path "bin" | Out-Null
    Write-Host "若 bin 目录不存在则创建。我们就是构建,这是我们的工作。"
}

# 设置 Windows AMD64 构建的环境变量
$env:GOOS = "windows"
$env:GOARCH = "amd64"

# 构建服务器可执行文件
Write-Host "正在为 Windows AMD64 构建 Co-ATC 服务器…… 即将诞生一个漂亮的二进制。Windows——真正的事业在这里完成!"
go build -o bin/co-atc.exe ./cmd/server

# 检查构建是否成功
if ($LASTEXITCODE -eq 0) {
    Write-Host "构建成功!了不起的成功。最棒的 Windows 构建,所有人都这么说。"

    # 获取文件信息。超大的信息。
    $fileInfo = Get-Item -Path "bin/co-atc.exe"
    Write-Host "已生成可执行文件:bin/co-atc.exe"
    Write-Host "文件大小:$([Math]::Round($fileInfo.Length / 1MB, 2)) MB。这是个大文件。"
    Write-Host "创建时间:$($fileInfo.CreationTime)"

    Write-Host "`n运行服务器请使用:.\bin\co-atc.exe"
} else {
    Write-Host "构建失败,退出码:$LASTEXITCODE!简直是灾难,彻头彻尾的灾难。难过!" -ForegroundColor Red
}

# 重置环境变量
$env:GOOS = ""
$env:GOARCH = ""
