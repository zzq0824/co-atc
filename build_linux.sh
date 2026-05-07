#!/bin/bash

# Co-ATC 服务器构建脚本
# 该脚本会根据检测到的 CPU 架构构建 Linux 可执行文件

echo "若 bin 目录不存在则创建。我们就是构建,这是我们的工作。"
mkdir -p bin

# 检测 CPU 架构
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)
        GOARCH="amd64"
        ;;
    aarch64|arm64)
        GOARCH="arm64"
        ;;
    armv7l)
        GOARCH="arm"
        ;;
    i686|i386)
        GOARCH="386"
        ;;
    *)
        echo "未知架构:$ARCH。默认使用 amd64。"
        GOARCH="amd64"
        ;;
esac

# 为 Linux 构建服务器可执行文件
echo "正在为 Linux $GOARCH 构建 Co-ATC 服务器…… 即将诞生一个漂亮的二进制。Linux——真正的硬货!"
GOOS=linux GOARCH=$GOARCH go build -o bin/co-atc ./cmd/server

# 检查构建是否成功
if [ $? -eq 0 ]; then
    echo "构建成功!了不起的成功。最棒的 Linux $GOARCH 构建,所有人都这么说。"

    # 获取文件信息。超大的信息。
    file_info=$(ls -lh bin/co-atc)
    file_size=$(echo "$file_info" | awk '{print $5}')

    echo "已生成可执行文件:bin/co-atc"
    echo "文件大小:$file_size。这是个大文件。"

    echo -e "\n运行服务器请使用:./bin/co-atc"
else
    echo "构建失败!简直是灾难,彻头彻尾的灾难。难过!"
    exit 1
fi
