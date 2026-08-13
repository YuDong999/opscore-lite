package ansible

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type SSEEvent struct {
	Type    string      `json:"type"`    // line, result, error, done
	Payload interface{} `json:"payload"`
}

type SSEWriter func(SSEEvent)

func (m *Manager) SSERunPlaybook(playbookID, inventoryID string, checkMode bool, tags, extraVars, limit string, forks int, emit SSEWriter) {
	playbook := m.GetPlaybook(playbookID)
	if playbook == nil {
		emit(SSEEvent{Type: "error", Payload: fmt.Sprintf("Playbook %s 不存在", playbookID)})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}
	m.sseRunPlaybook(playbook.Path, inventoryID, checkMode, tags, extraVars, limit, forks, emit)
}

func (m *Manager) SSERunAdhoc(hosts []string, inventoryID, module, args string, emit SSEWriter) {
	extraArgs := []string{"-m", module}
	if args != "" {
		extraArgs = append(extraArgs, "-a", args)
	}
	m.sseRunAnsible(hosts, inventoryID, emit, extraArgs...)
}

func (m *Manager) SSERunPing(hosts []string, inventoryID string, emit SSEWriter) {
	m.sseRunAnsible(hosts, inventoryID, emit, "-m", "ping")
}

func (m *Manager) sseRunAnsible(hosts []string, inventoryID string, emit SSEWriter, extraArgs ...string) {
	workDir, err := os.MkdirTemp("", "opscore-ansible-*")
	if err != nil {
		emit(SSEEvent{Type: "error", Payload: err.Error()})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}
	defer os.RemoveAll(workDir)

	inventoryPath, targetHosts, err := m.prepareInventory(workDir, hosts, inventoryID)
	if err != nil {
		emit(SSEEvent{Type: "error", Payload: err.Error()})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}

	args := []string{"-i", inventoryPath, "all"}
	args = append(args, extraArgs...)
	cmd := exec.Command("ansible", args...)

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	start := time.Now()

	if err := cmd.Start(); err != nil {
		emit(SSEEvent{Type: "error", Payload: err.Error()})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}

	outLines := []string{}
	scanDone := make(chan struct{}, 2)

	scan := func(rd *bufio.Reader, ch chan<- struct{}) {
		for {
			line, err := rd.ReadString('\n')
			if line != "" {
				line = strings.TrimRight(line, "\r\n")
				outLines = append(outLines, line)
				emit(SSEEvent{Type: "line", Payload: line})
			}
			if err != nil {
				break
			}
		}
		ch <- struct{}{}
	}

	go scan(bufio.NewReader(stdout), scanDone)
	go scan(bufio.NewReader(stderr), scanDone)

	cmd.Wait()
	<-scanDone
	<-scanDone
	elapsed := time.Since(start)

	out := strings.TrimSpace(strings.Join(outLines, "\n"))
	var results []Result
	if strings.HasPrefix(out, "{") {
		results = parseAnsibleJSON(out, targetHosts)
	} else if out != "" {
		results = parseAnsibleOutput(out, targetHosts)
	} else {
		results = []Result{{Host: "all", Success: false, Output: "无输出"}}
	}

	mod, arg := "", ""
	for i, a := range extraArgs {
		if a == "-m" && i+1 < len(extraArgs) { mod = extraArgs[i+1] }
		if a == "-a" && i+1 < len(extraArgs) { arg = extraArgs[i+1] }
	}
	rc := &RunContext{Hosts: hosts, InventoryID: inventoryID, Module: mod, Args: arg}
	m.saveHistory(adhocType(extraArgs), "", rc, results, elapsed)
	emit(SSEEvent{Type: "result", Payload: results})
	emit(SSEEvent{Type: "done", Payload: nil})
}

func (m *Manager) sseRunPlaybook(playbookPath, inventoryID string, checkMode bool, tags, extraVars, limit string, forks int, emit SSEWriter) {
	workDir, err := os.MkdirTemp("", "opscore-ansible-*")
	if err != nil {
		emit(SSEEvent{Type: "error", Payload: err.Error()})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}
	defer os.RemoveAll(workDir)

	inventoryPath, targetHosts, err := m.prepareInventory(workDir, nil, inventoryID)
	if err != nil {
		emit(SSEEvent{Type: "error", Payload: err.Error()})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}

	args := []string{"-i", inventoryPath, playbookPath, "--json"}
	if checkMode {
		args = append(args, "--check")
	}
	if tags != "" {
		args = append(args, "--tags", tags)
	}
	if extraVars != "" {
		args = append(args, "--extra-vars", extraVars)
	}
	if limit != "" {
		args = append(args, "--limit", limit)
	}
	if forks > 0 {
		args = append(args, "--forks", fmt.Sprintf("%d", forks))
	}

	cmd := exec.Command("ansible-playbook", args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	start := time.Now()

	if err := cmd.Start(); err != nil {
		emit(SSEEvent{Type: "error", Payload: err.Error()})
		emit(SSEEvent{Type: "done", Payload: nil})
		return
	}

	outLines := []string{}
	scanDone := make(chan struct{}, 2)

	scan := func(rd *bufio.Reader, ch chan<- struct{}) {
		for {
			line, err := rd.ReadString('\n')
			if line != "" {
				line = strings.TrimRight(line, "\r\n")
				outLines = append(outLines, line)
				emit(SSEEvent{Type: "line", Payload: line})
			}
			if err != nil {
				break
			}
		}
		ch <- struct{}{}
	}

	go scan(bufio.NewReader(stdout), scanDone)
	go scan(bufio.NewReader(stderr), scanDone)

	cmd.Wait()
	<-scanDone
	<-scanDone
	elapsed := time.Since(start)

	out := strings.TrimSpace(strings.Join(outLines, "\n"))
	var results []Result
	if strings.HasPrefix(out, "{") {
		results = parseAnsibleJSON(out, targetHosts)
	} else if out != "" {
		results = parseAnsibleOutput(out, targetHosts)
	} else {
		results = []Result{{Host: "all", Success: false, Output: "无输出"}}
	}

	rc := &RunContext{InventoryID: inventoryID, CheckMode: checkMode, Tags: tags, ExtraVars: extraVars, Limit: limit, Forks: forks}
	m.saveHistory("playbook", playbookPath, rc, results, elapsed)
	emit(SSEEvent{Type: "result", Payload: results})
	emit(SSEEvent{Type: "done", Payload: nil})
}

func adhocType(extraArgs []string) string {
	for i, a := range extraArgs {
		if a == "-m" && i+1 < len(extraArgs) {
			m := extraArgs[i+1]
			if m == "ping" {
				return "ping"
			}
			return "adhoc"
		}
	}
	return "adhoc"
}
