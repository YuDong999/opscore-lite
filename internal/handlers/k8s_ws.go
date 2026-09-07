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
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
)

// rePFRange 端口映射格式: local:remote 或单个端口 (local<=65535, remote<=65535)
var rePFRange = regexp.MustCompile(`^[0-9]{1,5}(:[0-9]{1,5})?$`)

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
	go k8sLogsStreamRun(conn, cluster, ns, pod, container, tail, follow, previous, timestamps)
}

func k8sLogsStreamRun(conn *websocket.Conn, cluster, ns, pod, container string, tail int64, follow, previous, timestamps bool) {
	defer conn.Close()
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

// K8sPortForwardWSHandler GET ?cluster=&ns=&pod=&res=&name=&ports=&address=
// 端口转发 (kubectl port-forward 等价): opscore 服务端监听本地端口 → SPDY 隧道 → Pod。
// 生命周期绑定 WS: ws 断开即关端口。帧: {"type":"ready"|"log"|"exit"|"error"}。
func K8sPortForwardWSHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, _ := queryCluster(r)
	if cluster == "" {
		writeErr(w, "cluster 必填", http.StatusBadRequest)
		return
	}
	res := q.Get("res")
	if res != "pods" && res != "services" {
		writeErr(w, "res 仅支持 pods/services", http.StatusBadRequest)
		return
	}
	ns, name := q.Get("ns"), q.Get("name")
	if name == "" || (ns != "" && !reK8sNamespace.MatchString(ns)) || !reK8sResName.MatchString(name) {
		writeErr(w, "name/ns 非法", http.StatusBadRequest)
		return
	}
	ports := q.Get("ports")
	if ports == "" {
		writeErr(w, "ports 必填 (如 8080:80)", http.StatusBadRequest)
		return
	}
	portList := strings.Split(ports, ",")
	for _, p := range portList {
		if !rePFRange.MatchString(strings.TrimSpace(p)) {
			writeErr(w, "ports 格式非法: "+p, http.StatusBadRequest)
			return
		}
	}
	addresses := []string{"127.0.0.1"}
	if a := q.Get("address"); a != "" {
		addresses = []string{a}
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[K8S-WS] port-forward upgrade: %v", err)
		return
	}
	defer conn.Close()
	var mu sync.Mutex

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
	// services → 解析到首个就绪后端 Pod (跟 kubectl port-forward svc 语义一致)
	podName := name
	if res == "services" {
		if ns == "" {
			ns = "default"
		}
		eps, e2 := cs.CoreV1().Endpoints(ns).Get(context.Background(), name, metav1.GetOptions{})
		if e2 != nil {
			wsSend(conn, &mu, wsFrame{Type: "error", Message: "service 无 Endpoints: " + e2.Error()})
			return
		}
		for _, ss := range eps.Subsets {
			for _, a := range ss.Addresses {
				if a.TargetRef != nil && a.TargetRef.Kind == "Pod" {
					podName = a.TargetRef.Name
					break
				}
			}
			if podName != name {
				break
			}
		}
		if podName == name {
			wsSend(conn, &mu, wsFrame{Type: "error", Message: "service 无就绪后端 Pod"})
			return
		}
	}
	if ns == "" {
		ns = "default"
	}
	log.Printf("[K8S-WS] port-forward start cluster=%s ns=%s pod=%s ports=%s addr=%v", cluster, ns, podName, ports, addresses)

	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(podName).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: "SPDY 初始化失败: " + err.Error()})
		return
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	out := &wsPFWriter{conn: conn, mu: &mu}
	errOut := &wsPFWriter{conn: conn, mu: &mu, isErr: true}
	fw, err := portforward.NewOnAddresses(dialer, addresses, portList, stopChan, readyChan, out, errOut)
	if err != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}
	done := make(chan error, 1)
	go func() { done <- fw.ForwardPorts() }()

	select {
	case <-readyChan:
		wsSend(conn, &mu, wsFrame{Type: "ready", Message: "转发已就绪 " + ports + " @ " + strings.Join(addresses, ",")})
	case <-time.After(20 * time.Second):
		close(stopChan)
		wsSend(conn, &mu, wsFrame{Type: "error", Message: "转发就绪超时"})
		return
	}

	// ws 断开 → 关端口; 客户端发 stop 也可
	stop := make(chan struct{})
	go func() {
		for {
			_, data, rerr := conn.ReadMessage()
			if rerr != nil {
				close(stop)
				return
			}
			var f wsFrame
			if json.Unmarshal(data, &f) == nil && f.Type == "stop" {
				close(stop)
				return
			}
		}
	}()
	select {
	case <-stop:
	case <-done:
	}
	close(stopChan)
	select {
	case perr := <-done:
		if perr != nil {
			wsSend(conn, &mu, wsFrame{Type: "error", Message: perr.Error()})
		}
	default:
	}
	wsSend(conn, &mu, wsFrame{Type: "exit", Code: 0})
	log.Printf("[K8S-WS] port-forward end cluster=%s pod=%s ports=%s", cluster, podName, ports)
}

// wsPFWriter 把 portforward 的 stdout/stderr 转成 ws "log" 帧
type wsPFWriter struct {
	conn  *websocket.Conn
	mu    *sync.Mutex
	isErr bool
}

func (w *wsPFWriter) Write(p []byte) (int, error) {
	t := "log"
	if w.isErr {
		t = "error"
	}
	if err := wsSend(w.conn, w.mu, wsFrame{Type: t, Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// K8sCpWSHandler GET ?cluster=&ns=&pod=&container=&direction=&podPath=&localPath=
// 文件互拷 (kubectl cp 等价): 走 SPDY exec + tar, 二进制帧流。
// from-pod: 服务端 tar cf - → ws 二进制流 (前端收组成文件); to-pod: ws 二进制流 → tar xmf -。
func K8sCpWSHandler(w http.ResponseWriter, r *http.Request) {
	if !pluginGuard(k8sPluginID, w) {
		return
	}
	q := r.URL.Query()
	cluster, _ := queryCluster(r)
	if cluster == "" {
		writeErr(w, "cluster 必填", http.StatusBadRequest)
		return
	}
	ns, pod := q.Get("ns"), q.Get("pod")
	if pod == "" || (ns != "" && !reK8sNamespace.MatchString(ns)) || !reK8sResName.MatchString(pod) {
		writeErr(w, "pod/ns 非法", http.StatusBadRequest)
		return
	}
	container, direction, podPath := q.Get("container"), q.Get("direction"), q.Get("podPath")
	if direction != "from-pod" && direction != "to-pod" {
		writeErr(w, "direction 仅支持 from-pod/to-pod", http.StatusBadRequest)
		return
	}
	if podPath == "" || len(podPath) > 1024 {
		writeErr(w, "podPath 非法", http.StatusBadRequest)
		return
	}
	if ns == "" {
		ns = "default"
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[K8S-WS] cp upgrade: %v", err)
		return
	}
	defer conn.Close()
	var mu sync.Mutex

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

	// tar 命令: from-pod → 打包到 stdout; to-pod → 从 stdin 解包到目录
	cmd := "tar xmf - -C '" + strings.TrimRight(podPath, "/") + "'"
	var stdout, stderr io.Writer
	var stdin io.Reader
	if direction == "from-pod" {
		cmd = "tar cf - '" + strings.TrimRight(podPath, "/") + "'"
		stdout = &wsTarWriter{conn: conn, mu: &mu}
		stderr = &wsPFWriter{conn: conn, mu: &mu}
	} else {
		// 浏览器无法组 tar: 先完整接收原始文件 → 打成单成员 tar → 再解包到目标目录
		baseName := q.Get("baseName")
		if baseName == "" || strings.Contains(baseName, "/") ||
			strings.Contains(baseName, "..") || len(baseName) > 200 {
			wsSend(conn, &mu, wsFrame{Type: "error", Message: "baseName 非法"})
			return
		}
		var raw bytes.Buffer
		for {
			mt, data, rerr := conn.ReadMessage()
			if rerr != nil {
				break
			}
			if mt == websocket.BinaryMessage {
				if raw.Len()+len(data) > 256<<20 {
					wsSend(conn, &mu, wsFrame{Type: "error", Message: "文件超过 256MB 上限"})
					return
				}
				_, _ = raw.Write(data)
			} else if mt == websocket.TextMessage {
				var f wsFrame
				if json.Unmarshal(data, &f) == nil && f.Type == "done" {
					break
				}
			}
		}
		var tarBuf bytes.Buffer
		tw := tar.NewWriter(&tarBuf)
		if err := tw.WriteHeader(&tar.Header{Name: baseName, Mode: 0644, Size: int64(raw.Len()), ModTime: time.Now()}); err == nil {
			_, _ = tw.Write(raw.Bytes())
		}
		_ = tw.Close()
		stdin = bytes.NewReader(tarBuf.Bytes())
		stderr = &wsPFWriter{conn: conn, mu: &mu}
	}
	log.Printf("[K8S-WS] cp start cluster=%s ns=%s pod=%s %s cmd=%q", cluster, ns, pod, direction, cmd)

	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{"/bin/sh", "-c", cmd},
			Stdin:     direction == "to-pod",
			Stdout:    direction == "from-pod",
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: err.Error()})
		return
	}
	sopt := remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr, Tty: false}
	if direction == "to-pod" {
		sopt.Stdin = stdin
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serr := exec.StreamWithContext(ctx, sopt)
	if serr != nil {
		wsSend(conn, &mu, wsFrame{Type: "error", Message: serr.Error()})
	}
	wsSend(conn, &mu, wsFrame{Type: "exit", Code: 0})
	log.Printf("[K8S-WS] cp end cluster=%s pod=%s %s err=%v", cluster, pod, direction, serr)
}

// wsTarWriter 把 tar stdout 二进制 → ws 二进制帧 (cp from-pod)
type wsTarWriter struct {
	conn *websocket.Conn
	mu   *sync.Mutex
}

func (w *wsTarWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
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
