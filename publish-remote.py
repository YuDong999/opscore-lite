#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把 opscore-lite 发到远端并重新部署(207.10 演示机)。

用法(在 Windows 开发机上):
    python publish-remote.py              # 构建 + 上传 + 部署 + 验证
    python publish-remote.py --skip-build # 只上传部署(用上一次构建的产物)
    python publish-remote.py --host 192.168.207.10

它做什么:
  1. 交叉编译 linux/amd64 的 opscore 与 agent(纯 Go, CGO 关掉, 本机 Windows 也能编);
  2. 打包 web/dist(含 modules/ 单模块页 —— 少发这个, 界面上的功能就会"源码里有、页面上没有");
  3. SFTP 传到远端 /tmp, 远端脚本里 **先备份** 再换文件;
  4. systemctl 重启 + 验证(拿"新版本才有的端点/字样"当判据, 不是只看进程活着)。

**只覆盖 二进制 / 前端 / agent 三样, 绝不碰 /opt/opscore/data**(那里是远端自己的连接、
审计、日志库)。备份留在 /opt/opscore/backups/<时间戳>/, 要回滚就把它拷回去。
"""
import argparse
import os
import posixpath
import subprocess
import sys
import tarfile
import time

ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "dist-linux")            # 构建产物暂存
HOST = "192.168.207.10"
PORT = 22
USER = "root"
PASSWORD = "123"

APP_DIR = "/opt/opscore"
APP_BIN = APP_DIR + "/opscore"
DIST_DIR = APP_DIR + "/web/dist"
AGENT_BIN = APP_DIR + "/bin/agent-linux-amd64"
SERVICE = "opscore"
STAGE = "/tmp/opscore-publish"

# 新版判据: 这些字样/端点只有新版才有, 用来确认"真的换上了"而不是"进程还在"
NEW_MARKERS = [
    ("engines 里出现 redis", "api/dbmanager/engines", '"type":"redis"'),
]


def run(cmd, **kw):
    print("+ " + " ".join(cmd) if isinstance(cmd, list) else "+ " + cmd)
    return subprocess.run(cmd, shell=isinstance(cmd, str), check=True,
                          cwd=ROOT, **kw)


def build():
    os.makedirs(OUT, exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64")
    print("==> 1/4 交叉编译 linux/amd64")
    for dst, pkg, tags in [
        (os.path.join(OUT, "opscore-linux-amd64"), ".", ["gonavi_sqlite_driver"]),
        (os.path.join(OUT, "agent-linux-amd64"), "./cmd/agent", []),
    ]:
        cmd = ["go", "build", "-ldflags=-s -w", "-o", dst]
        if tags:
            cmd += ["-tags", ",".join(tags)]
        cmd.append(pkg)
        print("+ " + " ".join(cmd))
        subprocess.run(cmd, cwd=ROOT, env=env, check=True)
        print("    -> %s (%.1f MB)" % (os.path.basename(dst), os.path.getsize(dst) / 1048576))

    print("==> 2/4 打包前端 web/dist")
    dist = os.path.join(ROOT, "web", "dist")
    if not os.path.isfile(os.path.join(dist, "index.html")):
        sys.exit("web/dist 没有构建产物 —— 先跑: cd web && npm run build && node scripts/build-module.mjs dbmanager")
    tgz = os.path.join(OUT, "web-dist.tar.gz")
    with tarfile.open(tgz, "w:gz") as tf:
        tf.add(dist, arcname="dist")
    # 校验包内顶层就是 dist/(远端解压依赖这个结构)
    with tarfile.open(tgz) as tf:
        tops = {n.split("/")[0] for n in tf.getnames()}
    if tops != {"dist"}:
        sys.exit("打包结构不对(顶层应为 dist/): %s" % sorted(tops))
    print("    -> %s (%.1f MB)" % (os.path.basename(tgz), os.path.getsize(tgz) / 1048576))
    # 亮一下模块页, 确认单模块页也打进去了(踩过: 只发 SPA 会漏)
    mods = os.path.join(dist, "modules")
    if os.path.isdir(mods):
        print("    含模块页: " + ", ".join(sorted(os.listdir(mods))))
    else:
        print("    [警告] dist/modules 不存在, 单模块页没打进来")


def upload():
    import paramiko
    print("==> 3/4 上传到 %s:%s" % (HOST, STAGE))
    cli = paramiko.SSHClient()
    cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    cli.connect(HOST, PORT, USER, PASSWORD, timeout=20, allow_agent=False, look_for_keys=False)
    try:
        cli.exec_command("mkdir -p " + STAGE)[1].channel.recv_exit_status()
        sftp = cli.open_sftp()
        for name in ["opscore-linux-amd64", "agent-linux-amd64", "web-dist.tar.gz"]:
            lp = os.path.join(OUT, name)
            print("    %s (%.1f MB)" % (name, os.path.getsize(lp) / 1048576))
            sftp.put(lp, posixpath.join(STAGE, name))
        sftp.close()
    finally:
        cli.close()


REMOTE_SCRIPT = r"""
set -e
cd /opt/opscore
TS=$(date +%Y%m%d-%H%M%S)
BK="/opt/opscore/backups/$TS"
mkdir -p "$BK"

echo "==> 备份当前版本 -> $BK"
cp -a /opt/opscore/opscore "$BK/opscore" 2>/dev/null || true
if [ -d /opt/opscore/web/dist ]; then
  tar czf "$BK/web-dist.tar.gz" -C /opt/opscore/web dist
fi
cp -a /opt/opscore/bin/agent-linux-amd64 "$BK/agent-linux-amd64" 2>/dev/null || true
echo "    备份: $(du -sh "$BK" | cut -f1)"

echo "==> 停服务"
systemctl stop opscore
sleep 1

echo "==> 换文件"
install -m 0755 /tmp/opscore-publish/opscore-linux-amd64 /opt/opscore/opscore
# 前端: 包里是 dist/ 开头, 解到 web/ 下自然就是 web/dist/(**别加 --strip-components**,
# 加了会把 dist 这一层剥掉, 文件落到 web/ 顶层, 前端就"发了但没生效")
rm -rf /opt/opscore/web/dist
tar xzf /tmp/opscore-publish/web-dist.tar.gz -C /opt/opscore/web
install -m 0755 /tmp/opscore-publish/agent-linux-amd64 /opt/opscore/bin/agent-linux-amd64

echo "==> 启服务"
systemctl start opscore
sleep 3
systemctl is-active opscore || { echo "[FAIL] 服务没起来"; journalctl -u opscore -n 20 --no-pager; exit 1; }

echo "==> 验证"
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -sf --max-time 3 http://127.0.0.1:8088/api/dbmanager/engines >/tmp/eng.json 2>/dev/null; then break; fi
  sleep 2
done
if grep -q '"type":"redis"' /tmp/eng.json; then
  echo "    [OK] 新版已生效(engines 里有 redis)"
else
  echo "    [FAIL] engines 里没有 redis —— 可能后端没换成功"
  head -c 300 /tmp/eng.json; echo
  exit 1
fi
# 前端验证不能只看后端字符串 —— "发了但没生效"就是这么漏过去的:
#  1) dist/index.html 必须存在; 2) 它引用的入口 js 必须真能 HTTP 200 取到;
#  3) 单模块页也要能取到(它不在 SPA 构建产物里, 最容易漏)
[ -f /opt/opscore/web/dist/index.html ] || { echo "[FAIL] /opt/opscore/web/dist/index.html 不存在"; ls -la /opt/opscore/web/; exit 1; }
JS=$(grep -o 'assets/index-[A-Za-z0-9_-]*\.js' /opt/opscore/web/dist/index.html | head -1)
CSS=$(grep -o 'assets/index-[A-Za-z0-9_-]*\.css' /opt/opscore/web/dist/index.html | head -1)
echo "    前端入口: $JS  $CSS"
for f in "$JS" "$CSS"; do
  [ -n "$f" ] || { echo "[FAIL] index.html 里没找到入口资源"; exit 1; }
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:8088/$f")
  if [ "$code" != "200" ]; then echo "[FAIL] $f 取不到 (HTTP $code)"; exit 1; fi
done
echo "    [OK] 入口资源均可访问"
MODJS=$(grep -o 'assets/index-[A-Za-z0-9_-]*\.js' /opt/opscore/web/dist/modules/dbmanager/module.html 2>/dev/null | head -1)
if [ -n "$MODJS" ]; then
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:8088/modules/dbmanager/$MODJS")
  [ "$code" = "200" ] && echo "    [OK] 模块页可取: dbmanager/$MODJS" || { echo "[FAIL] 模块页入口取不到 (HTTP $code)"; exit 1; }
else
  echo "    [警告] dbmanager 模块页缺失(只发了 SPA?)"
fi
echo "    服务: $(systemctl is-active opscore)  二进制: $(ls -l /opt/opscore/opscore | awk '{print $5" bytes "$6" "$7" "$8}')"
echo "    dist/assets 文件数: $(ls -1 /opt/opscore/web/dist/assets | wc -l)"

# 清理 /opt/opscore/backups 只留 N 份 —— 每次部署备份约 90MB, 不清理会一直涨
KEEP_BK=5
ls -1dt /opt/opscore/backups/*/ 2>/dev/null | tail -n +$((KEEP_BK+1)) | while read -r d; do
  rm -rf "$d"
  echo "    已清理旧备份: $(basename "$d")"
done
echo "    /opt/opscore/backups 保留: $(ls -1d /opt/opscore/backups/*/ 2>/dev/null | wc -l) 份, 共 $(du -sh /opt/opscore/backups 2>/dev/null | cut -f1)"

"""


def deploy():
    import paramiko
    print("==> 4/4 远端部署并验证")
    cli = paramiko.SSHClient()
    cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    cli.connect(HOST, PORT, USER, PASSWORD, timeout=20, allow_agent=False, look_for_keys=False)
    try:
        stdin, stdout, stderr = cli.exec_command(REMOTE_SCRIPT, timeout=300)
        out = stdout.read().decode("utf-8", "replace")
        err = stderr.read().decode("utf-8", "replace")
        code = stdout.channel.recv_exit_status()
        print(out.rstrip())
        if err.strip():
            print("[stderr] " + err.rstrip()[:1500])
        if code != 0:
            sys.exit("远端部署失败(退出码 %d)" % code)
    finally:
        cli.close()


def main():
    global HOST, PASSWORD
    ap = argparse.ArgumentParser()
    ap.add_argument("--skip-build", action="store_true", help="跳过构建, 用 dist-linux/ 里现有产物")
    ap.add_argument("--host", default=HOST)
    ap.add_argument("--password", default=PASSWORD)
    a = ap.parse_args()
    HOST, PASSWORD = a.host, a.password

    if not a.skip_build:
        build()
    else:
        print("==> 跳过构建, 复用 dist-linux/")
    upload()
    deploy()
    print("\n完成。浏览器访问 http://%s/#/dbmanager (记得 Ctrl+F5 硬刷)" % HOST)
    print("回滚: 远端 /opt/opscore/backups/ 下最近那个目录, 把 opscore 与 web-dist.tar.gz 拷回去再 restart")


if __name__ == "__main__":
    main()
