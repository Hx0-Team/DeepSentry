#!/bin/bash

# ==========================================
# DeepSentry 一键交叉编译脚本 
# ==========================================

# 项目名称
APP_NAME="deepsentry"


MAIN_FILE="./cmd"

# 输出目录
OUTPUT_DIR="build"

# 编译参数 (-s -w 减小体积)
LDFLAGS="-s -w"

# 清理旧文件
echo "🧹 正在清理旧文件..."
rm -rf $OUTPUT_DIR
mkdir -p $OUTPUT_DIR

echo "🚀 开始编译全平台版本..."
echo "------------------------------------------"

# --- 目标平台列表 ---
platforms=(
    "darwin/amd64"  # Mac Intel
    "darwin/arm64"  # Mac Apple Silicon
    "linux/amd64"   # Linux x64
    "linux/arm64"   # Linux ARM64
    "linux/386"     # Linux x86
    "windows/amd64" # Windows x64
    "windows/386"   # Windows x86
)

for platform in "${platforms[@]}"
do
    # 拆分 OS 和 ARCH
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    
    # 生成文件名
    output_name=$APP_NAME'-'$GOOS'-'$GOARCH
    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
    fi

    echo "🔨 Building for $GOOS/$GOARCH ..."

    # 执行编译
    # 注意：这里使用 $MAIN_FILE (即 ./cmd)
    env CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -ldflags "$LDFLAGS" -o $OUTPUT_DIR/$output_name $MAIN_FILE

    if [ $? -ne 0 ]; then
        echo "❌ 编译失败: $GOOS/$GOARCH"
        exit 1
    fi
done

echo "------------------------------------------"
echo "✅ 全部编译完成！文件位于 $OUTPUT_DIR/ 目录下"
ls -lh $OUTPUT_DIR