#!/bin/bash
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

REPO="YuDong999/opscore-lite"
INSTALL_DIR="/opt/opscore"
BACKUP_DIR="${INSTALL_DIR}/data/backups"

err()  { echo -e "${RED}错误: $*${NC}" >&2; exit 1; }
info() { echo -e "${CYAN}[信息] $*${NC}"; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }

# ========== 检查 root ==========
if [[ $EUID -ne 0 ]]; then
  err "需要 root 权限执行（systemd 安装需要），请 sudo bash $0"
fi

# ========== 检测平台 ==========
case "$(uname -s)" in
  Linux*)  os="linux" ;;
  Darwin*) os="darwin" ;;
  *)       err "不支持的系统: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  armv7l|armhf)  err "不支持 ARMv7" ;;
  *)             err "不支持的架构: $(uname -m)" ;;
esac

platform="${os}-${arch}"
info "检测到平台: ${platform}"

# ========== 获取最新版本 ==========
info "获取最新版本..."
version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
[[ -z "$version" ]] && err "获取版本失败，请检查网络连接"
ok "最新版本: ${version}"

# ========== 下载 ==========
archive_name="opscore-${os}-${arch}.tar.gz"
download_url="https://github.com/${REPO}/releases/download/${version}/${archive_name}"

tmp_dir=$(mktemp -d)
trap "rm -rf ${tmp_dir}" EXIT

info "下载: ${download_url}"
curl -fsSL "${download_url}" -o "${tmp_dir}/${archive_name}"
ok "下载完成"

# ========== 解压 ==========
info "解压..."
cd "${tmp_dir}"
tar xzf "${archive_name}"
ok "解压完成"

# ========== 备份旧版本 ==========
if [[ -f "${INSTALL_DIR}/opscore" ]] || [[ -d "${INSTALL_DIR}/web" ]]; then
  mkdir -p "${BACKUP_DIR}"
  backup_name="opscore.$(date +%Y%m%d_%H%M%S)"
  backup_path="${BACKUP_DIR}/${backup_name}"
  info "检测到旧版本，备份到 ${backup_path}..."
  cd "${INSTALL_DIR}"
  tar czf "${backup_path}.tar.gz" opscore web/dist 2>/dev/null || true
  ok "备份完成: ${backup_path}.tar.gz"
fi

# ========== 部署 ==========
info "部署到 ${INSTALL_DIR}..."
mkdir -p "${INSTALL_DIR}/web/dist"

# 二进制在 release/ 目录里命名 opscore-linux-amd64 等
cd "${tmp_dir}"
bin_src=$(ls release/opscore* 2>/dev/null | head -1)
[[ -z "$bin_src" ]] && err "找不到二进制文件"
cp "$bin_src" "${INSTALL_DIR}/opscore"
chmod +x "${INSTALL_DIR}/opscore"

cp -r release/web/dist/* "${INSTALL_DIR}/web/dist/"
ok "部署完成"

# ========== PATH ==========
cat > /etc/profile.d/opscore.sh <<EOF
export PATH=${INSTALL_DIR}:\$PATH
EOF
source /etc/profile.d/opscore.sh
ok "PATH 已加入 /etc/profile.d/opscore.sh"

# ========== systemd unit ==========
info "安装 systemd service..."
cat > /etc/systemd/system/opscore.service << 'SERVICE'
[Unit]
Description=OpsCore Demo (单二进制运维控制台)
After=network.target

[Service]
Type=simple
ExecStart=/opt/opscore/opscore -addr 127.0.0.1:8088 -dist /opt/opscore/web/dist
Environment=OPCORE_ADDR=127.0.0.1:8088
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
SERVICE
systemctl daemon-reload
ok "systemd unit 已安装"

# ========== 询问启动 ==========
echo ""
read -p "是否现在启动服务？(y/n): " -n 1 -r
echo ""
if [[ $REPLY =~ ^[Yy]$ ]]; then
  systemctl enable --now opscore
  sleep 1
  if systemctl is-active --quiet opscore; then
    ok "服务已启动"
  else
    echo -e "${YELLOW}! 服务启动失败，请查看日志: journalctl -u opscore -e${NC}"
  fi
else
  echo "已跳过启动。后续可执行:"
  echo "  sudo systemctl enable --now opscore"
fi

# ========== 汇总信息 ==========
echo ""
echo -e "${GREEN}=========================================="
echo "  部署完成"
echo "==========================================${NC}"
echo ""
echo "  安装目录:   ${INSTALL_DIR}"
echo "  数据目录:   ${INSTALL_DIR}/data"
echo "  服务名称:   opscore"
echo ""
echo "  访问地址:"
echo "    直连:     http://<服务器IP>:8088"
echo "    nginx:    http://<服务器IP>:8081"
echo "    本地:     http://127.0.0.1:8088"
echo ""
echo "  服务管理:"
echo "    状态:     systemctl status opscore"
echo "    启动:     systemctl start opscore"
echo "    停止:     systemctl stop opscore"
echo "    重启:     systemctl restart opscore"
echo "    日志:     journalctl -u opscore -f"
echo "    自启:     systemctl enable/disable opscore"
echo ""
echo "  PATH:       /etc/profile.d/opscore.sh (重登录生效)"
echo "  手动执行:   ${INSTALL_DIR}/opscore -h"
echo ""
