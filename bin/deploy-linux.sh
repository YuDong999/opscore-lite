#!/bin/bash
# opscore-lite 一键部署/修复脚本
# 用法: bash <本脚本绝对路径>   (在 Linux 上执行, 本机或目标机均可)
# 自动探测 systemd 服务实际安装路径, 替换新二进制+多机前端+agent, 重启并验证
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_BIN="$SCRIPT_DIR/opscore-linux-amd64"
SRC_DIST="$SCRIPT_DIR/web/dist"
SRC_AGENT="$SCRIPT_DIR/agent-linux-amd64"
SERVICE=opscore

echo "==> 1/6 检查源文件 ($SCRIPT_DIR)"
for f in "$SRC_BIN" "$SRC_DIST/index.html" "$SRC_AGENT"; do
    [ -f "$f" ] || { echo "缺少: $f"; exit 1; }
done
echo "    二进制: $(ls -l "$SRC_BIN" | awk '{print $5" bytes, "$6" "$7" "$8}')"

echo "==> 2/6 探测服务安装路径"
RAW=$(systemctl show "$SERVICE" -p ExecStart --no-pager 2>/dev/null)
[ -n "$RAW" ] || { echo "未找到服务 $SERVICE, 请确认已用 systemd 部署"; exit 1; }
APP_BIN=$(echo "$RAW" | sed 's/.*path=\([^;]*\);.*/\1/' | tr -d ' ')
DIST_DIR=$(echo "$RAW" | grep -o -- '-dist [^ ]*' | awk '{print $2}')
DIST_DIR=${DIST_DIR:-"$(dirname "$APP_BIN")/web/dist"}
APP_DIR=$(dirname "$APP_BIN")
echo "    服务: $SERVICE"
echo "    二进制: $APP_BIN"
echo "    前端目录: $DIST_DIR"
[ -n "$DIST_DIR" ] && [ -d "$(dirname "$DIST_DIR")" ] || { echo "前端目录异常: $DIST_DIR"; exit 1; }

echo "==> 3/6 停止服务"
systemctl stop "$SERVICE"

echo "==> 4/6 部署文件"
mkdir -p "$DIST_DIR" "$APP_DIR/bin"
cp -f "$SRC_BIN" "$APP_BIN"
chmod +x "$APP_BIN"
rm -f "$DIST_DIR/assets/index-D0KEAjXX.js" "$DIST_DIR/assets/index-PC1i5zQl.css"
cp -rf "$SRC_DIST/." "$DIST_DIR/"
cp -f "$SRC_AGENT" "$APP_DIR/bin/agent-linux-amd64"
chmod +x "$APP_DIR/bin/agent-linux-amd64"
echo "    二进制 -> $APP_BIN"
echo "    前端   -> $DIST_DIR (多机版)"
echo "    agent  -> $APP_DIR/bin/agent-linux-amd64"

echo "==> 5/6 启动服务"
systemctl start "$SERVICE"
sleep 2
systemctl is-active "$SERVICE"

echo "==> 6/6 验证前端"
JS=$(curl -s http://127.0.0.1:8088/ | grep -o 'assets/index-[A-Za-z0-9]*\.js' || true)
echo "    当前加载: $JS"
case "$JS" in
    *DCGmlqf5*) echo "    [OK] 多机版前端已生效" ;;
    *) echo "    [FAIL] 不是多机前端, 检查是否还在缓存或端口被占用"; systemctl status "$SERVICE" --no-pager -l | tail -5 ;;
esac

echo "==> 完成. 浏览器访问 http://<本机IP>:8088 应能看到左侧主机列表"
