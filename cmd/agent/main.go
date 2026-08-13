package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	server := flag.String("server", "ws://127.0.0.1:8088", "OpsCore 服务端 WebSocket 地址")
	hostID := flag.String("host-id", "", "本机在 OpsCore 中的主机 ID")
	flag.Parse()

	log.Printf("OpsCore Agent 启动, server=%s, hostID=%s", *server, *hostID)

	if *hostID == "" {
		log.Fatal("--host-id 不能为空")
	}

	c := newCollector()
	wc := newWSClient(*server, *hostID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go c.run(wc.pushSnapshot)

	stop := make(chan struct{})
	go func() {
		<-sigCh
		close(stop)
	}()

	wc.run(stop)
}
