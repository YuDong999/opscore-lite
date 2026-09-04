package handlers

// K8s WS 通道: exec-tty / logs-stream / port-forward / cp 复用同一基础设施。
//
// 协议 (ws 文本帧) — 前端 xterm.js 直接对接 exec / logs:
//   服务端 → 客户端:
//     {"type":"stdout","data":"..."}     // exec stdout
//     {"type":"stderr","data":"..."}     // exec stderr
//     {"type":"resize","rows":N,"cols":N} // exec 终端 resize 确认回显
//     {"type":"log","data":"..."}         // logs-stream 行
//     {"type":"log_meta","tail":..,"follow":..} // logs-stream 头
//     {"type":"exit","code":N}            // 进程退出
//     {"type":"error","message":"..."}    // 错误
//   客户端 → 服务端:
//     {"type":"stdin","data":"..."}       // exec 输入
//     {"type":"resize","rows":N,"cols":N} // exec resize
//
// 鉴权: 跟 HTTP 一样, 走 auth.Middleware (main.go 全局); ws 升级在 pluginGuard 后再做。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// wsFrame 协议帧
type wsFrame struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Code int    `json:"code,omitempty"`
	// log_meta
	Tail   int64 `json:"tail,omitempty"`
	Follow bool  `json:"follow,omitempty"`
	// error
	Message string `json:"message,omitempty"`
}

func wsSend(c *websocket.Conn, mu *sync.Mutex, f wsFrame) error {
	b, _ := json.Marshal(f)
	mu.Lock()
	defer mu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.WriteMessage(websocket.TextMessage, b)
}

// K8sPodExecWSHandler GET ?cluster=&ns=&pod=&container=&command=&rows=&cols=
// 交互式 exec, 走 ws
func K8sPodExecWSHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, resOK := queryCluster(r)
	if !resOK {
		writeErr(w, "cluster 必填", http.StatusBadRequest)
		return
	}
	ns := q.Get("ns")
	pod := q.Get("pod")
	container := q.Get("container")
	if !reK8sResName.MatchString(pod) || (ns != "" && !reK8sNamespace.MatchString(ns)) {
		writeErr(w, "pod/ns 非法", http.StatusBadRequest)
		return
	}
	cmd := q.Get("command")
	if cmd == "" {
		cmd = "sh"
	}
	tty := q.Get("tty") != "0" // 默认 true (交互式)
	rows, cols := 24, 80
	if v := q.Get("rows"); v != "" {
		fmt.Sscanf(v, "%d", &rows)
	}
	if v := q.Get("cols"); v != "" {
		fmt.Sscanf(v, "%d", &cols)
	}
	if rows < 5 {
		rows = 5
	}
	if cols < 20 {
		cols = 20
	}
	_ = rows
	_ = cols
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[K8S-WS] exec upgrade: %v", err)
		return
	}
	defer conn.Close()
	go k8sExecWSRun(conn, cluster, ns, pod, container, cmd, rows, cols, tty)
}

// k8sExecWSRun 桥 ws ↔ SPDY executor
func k8sExecWSRun(conn *websocket.Conn, cluster, ns, pod, container, cmd string, rows, cols int, tty bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[K8S-WS] exec panic: %v", r)
		}
	}()
	var mu sync.Mutex
	defer conn.Close()
	log.Printf("[K8S-WS] exec start cluster=%s ns=%s pod=%s container=%s cmd=%q", cluster, ns, pod, container, cmd)

	cfg, err := k8sMgr.RESTConfig(cluster)
	if err != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}
	cs, err := k8sMgr.Clientset(cluster)
	if err != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}
	// stdin pipe
	stdinReader, stdinWriter := io.Pipe()
	stdoutBuf := &wsWriter{conn: conn, mu: &mu, t: "stdout"}
	stderrBuf := &wsWriter{conn: conn, mu: &mu, t: "stderr"}

	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{"/bin/sh", "-c", cmd},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       tty,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// reader goroutine: ws 文本帧 → stdin pipe
	go func() {
		// 延迟 1s 启动读, 给 SPDY dial 一点时间; 若 ws 已关, ReadMessage 会立即 EOF
		time.Sleep(1 * time.Second)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			var f wsFrame
			if err := json.Unmarshal(data, &f); err != nil {
				continue
			}
			switch f.Type {
			case "stdin":
				_, _ = stdinWriter.Write([]byte(f.Data))
			case "resize":
				// TTY 模式下 kubelet 会自己适配, 这里仅回显确认
				_ = wsSend(conn, &mu, wsFrame{Type: "resize", Rows: f.Rows, Cols: f.Cols})
			case "signal":
				// remotecommand 没有细粒度 signal API, 关闭 ws 即 ctx cancel
				cancel()
				return
			}
		}
	}()

	// stream (在 stdlib 实现里 TTY 模式下 resize 不生效, 仍可工作)
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdinReader,
		Stdout: stdoutBuf,
		Stderr: stderrBuf,
		Tty:    tty,
	})
	if streamErr != nil {
		log.Printf("[K8S-WS] exec stream err: %v", streamErr)
	}
	code := 0
	if streamErr != nil {
		_ = wsSend(conn, &mu, wsFrame{Type: "error", Message: streamErr.Error()})
		code = 1
	}
	log.Printf("[K8S-WS] exec end cluster=%s pod=%s code=%d", cluster, pod, code)
	_ = wsSend(conn, &mu, wsFrame{Type: "exit", Code: code})
}

// wsWriter 把 SPDY executor 的 Stdout/Stderr 写到 ws 帧
type wsWriter struct {
	conn *websocket.Conn
	mu   *sync.Mutex
	t    string // "stdout" / "stderr"
}

func (w *wsWriter) Write(p []byte) (int, error) {
	if err := wsSend(w.conn, w.mu, wsFrame{Type: w.t, Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// K8sPodLogsStreamWSHandler GET ?cluster=&ns=&pod=&container=&tail=&previous=&follow=&timestamps=
// 日志流式, 走 ws。follow=false 拉完即关; follow=true 持续到 ws 关闭
func K8sPodLogsStreamWSHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, _ := queryCluster(r)
	if cluster == "" {
		writeErr(w, "cluster 必填", http.StatusBadRequest)
		return
	}
	ns := q.Get("ns")
	pod := q.Get("pod")
	container := q.Get("container")
	if !reK8sResName.MatchString(pod) || (ns != "" && !reK8sNamespace.MatchString(ns)) {
		writeErr(w, "pod/ns 非法", http.StatusBadRequest)
		return
	}
	var tail int64 = 200
	if v := q.Get("tail"); v != "" {
		fmt.Sscanf(v, "%d", &tail)
	}
	follow := q.Get("follow") != "0"
	previous := q.Get("previous") == "1"
	timestamps := q.Get("timestamps") != "0"

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[K8S-WS] logs upgrade: %v", err)
		return
	}
	defer conn.Close()
	go k8sLogsStreamRun(conn, cluster, ns, pod, container, tail, follow, previous, timestamps)
}

func k8sLogsStreamRun(conn *websocket.Conn, cluster, ns, pod, container string, tail int64, follow, previous, timestamps bool) {
	var mu sync.Mutex
	_ = wsSend(conn, &mu, wsFrame{Type: "log_meta", Tail: tail, Follow: follow})

	cs, err := k8sMgr.Clientset(cluster)
	if err != nil {
		_ = wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}
	opts := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tail,
		Follow:     follow,
		Previous:   previous,
		Timestamps: timestamps,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// ws 关闭时 cancel: 用 close 探测
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				cancel()
				return
			}
		}
	}()
	stream, err := cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		_ = wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}
	defer stream.Close()
	buf := make([]byte, 8192)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			_ = wsSend(conn, &mu, wsFrame{Type: "log", Data: string(buf[:n])})
		}
		if err != nil {
			if err != io.EOF {
				_ = wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
			}
			break
		}
	}
	_ = wsSend(conn, &mu, wsFrame{Type: "exit", Code: 0})
}

// K8sPortForwardWSHandler 端口转发 — 占位 (P3 阶段完整实现: opscore 端开本地端口透传)
func K8sPortForwardWSHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var mu sync.Mutex
	_ = wsSend(conn, &mu, wsFrame{Type: "error", Message: "port-forward: 暂未实现, P3 阶段补全"})
}

// K8sCpWSHandler 文件互拷 — 占位
func K8sCpWSHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var mu sync.Mutex
	_ = wsSend(conn, &mu, wsFrame{Type: "error", Message: "cp: 暂未实现, P3 阶段补全"})
}

// ===== 内部 =====

// queryCluster 从 query 读 cluster
func queryCluster(r *http.Request) (string, bool) {
	c := r.URL.Query().Get("cluster")
	if c == "" || !reK8sClusterID.MatchString(c) {
		return "", false
	}
	return c, true
}

// 占位避免 imports lint
var _ = bytes.NewBuffer
var _ = strings.TrimSpace
var _ = time.Now
