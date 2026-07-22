#!/bin/bash
# ==========================================
# OpsCore 一键安装脚本
#
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/YuDong999/opscore-lite/main/install.sh | bash
#
# 功能:
#   - 自动检测平台 (linux/mac, amd64/arm64)
#   - 从 GitHub Releases 下载预构建二进制 + web/dist
#   - 安装到 /usr/local/bin/opscore (需要 sudo)
#   - 或安装到 ~/.local/bin/opscore (无需 sudo)
# ==========================================

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

REPO="YuDong999/opscore-lite"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/opt/opscore/data"

# ========== 检测平台 ==========
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux*)     os="linux" ;;
        Darwin*)    os="darwin" ;;
        MINGW*|MSYS*|CYGWIN*)  os="windows" ;;
        *)
            echo -e "${RED}错误: 不支持的操作系统 $(uname -s)${NC}"
            exit 1
            ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)   arch="amd64" ;;
        aarch64|arm64)   arch="arm64" ;;
        armv7l|armhf)
            echo -e "${RED}错误: 不支持 ARMv7 架构${NC}"
            exit 1
            ;;
        *)
            echo -e "${RED}错误: 不支持的架构 $(uname -m)${NC}"
            exit 1
            ;;
    esac

    echo "${os}-${arch}"
}

# ========== 获取最新版本 ==========
get_latest_version() {
    local version
    version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    if [ -z "$version" ]; then
        echo -e "${RED}错误: 无法获取最新版本${NC}"
        echo "请检查网络连接或访问 https://github.com/${REPO}/releases"
        exit 1
    fi
    echo "$version"
}

# ========== 下载并安装 ==========
install_opscore() {
    local platform="$1"
    local version="$2"
    local os arch archive_name download_url tmp_dir

    os=$(echo "$platform" | cut -d'-' -f1)
    arch=$(echo "$platform" | cut -d'-' -f2)

    if [ "$os" = "windows" ]; then
        archive_name="opscore-${os}-${arch}.zip"
    else
        archive_name="opscore-${os}-${arch}.tar.gz"
    fi

    download_url="https://github.com/${REPO}/releases/download/${version}/${archive_name}"

    echo -e "${CYAN}[信息] 下载: ${download_url}${NC}"
    tmp_dir=$(mktemp -d)
    trap "rm -rf $tmp_dir" EXIT

    if ! curl -fsSL "$download_url" -o "${tmp_dir}/${archive_name}"; then
        echo -e "${RED}错误: 下载失败${NC}"
        echo "请检查版本号或网络连接"
        exit 1
    fi

    echo -e "${CYAN}[信息] 解压中...${NC}"
    cd "$tmp_dir"
    if [ "$os" = "windows" ]; then
        unzip -q "$archive_name"
    else
        tar xzf "$archive_name"
    fi

    # 安装二进制
    echo -e "${CYAN}[信息] 安装到 ${INSTALL_DIR}/opscore ...${NC}"
    mkdir -p "$INSTALL_DIR"
    if [ -w "$INSTALL_DIR" ] 2>/dev/null; then
        cp release/opscore* "$INSTALL_DIR/opscore"
    else
        sudo cp release/opscore* "$INSTALL_DIR/opscore"
    fi
    chmod +x "${INSTALL_DIR}/opscore"

    # 安装 web/dist
    local dist_dir="${INSTALL_DIR}/../lib/opscore/web/dist"
    mkdir -p "$dist_dir"
    if [ -w "$(dirname "$dist_dir")" ] 2>/dev/null; then
        cp -r release/web/dist/* "$dist_dir/"
    else
        sudo cp -r release/web/dist/* "$dist_dir/"
    fi

    echo -e "${GREEN}✓ 安装完成${NC}"
}

# ========== 主流程 ==========
main() {
    echo ""
    echo -e "${GREEN}=========================================="
    echo "  OpsCore 一键安装"
    echo "==========================================${NC}"
    echo ""

    # 检测平台
    platform=$(detect_platform)
    echo -e "${CYAN}[信息] 平台: ${platform}${NC}"

    # 获取最新版本
    version=$(get_latest_version)
    echo -e "${CYAN}[信息] 版本: ${version}${NC}"
    echo ""

    # 下载并安装
    install_opscore "$platform" "$version"

    echo ""
    echo -e "${GREEN}=========================================="
    echo "  安装完成！"
    echo "==========================================${NC}"
    echo ""
    echo "快速开始:"
    echo "  opscore                              # 启动服务 (默认 :8088)"
    echo "  opscore -addr 127.0.0.1:8088         # 指定监听地址"
    echo "  opscore -dist /path/to/web/dist      # 指定前端目录"
    echo ""
    echo "开机自启 (systemd):"
    echo "  sudo cp deploy/opscore.service /etc/systemd/system/"
    echo "  sudo systemctl daemon-reload && sudo systemctl enable --now opscore"
    echo ""
    echo "查看帮助:"
    echo "  opscore -h"
    echo ""
}

main "$@"
