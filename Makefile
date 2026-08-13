BIN_DIR ?= bin
LDFLAGS ?= -ldflags="-s -w"

.PHONY: all agent agent-all server linux clean

all: agent server

server:
	go build $(LDFLAGS) -o $(BIN_DIR)/opscore-server .

agent: agent-linux-amd64

agent-all: agent-linux-amd64 agent-linux-arm64 agent-windows-amd64

agent-linux-amd64:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/agent-linux-amd64 ./cmd/agent

agent-linux-arm64:
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BIN_DIR)/agent-linux-arm64 ./cmd/agent

agent-windows-amd64:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/agent-windows-amd64.exe ./cmd/agent

linux: agent-linux-amd64
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/opscore-linux-amd64 .

clean:
	rm -f $(BIN_DIR)/agent-* $(BIN_DIR)/opscore-server
