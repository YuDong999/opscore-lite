package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
)

type CronEntry struct {
	ID      string `json:"id"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Comment  string `json:"comment"`
	Enabled  bool   `json:"enabled"`
	Line     int    `json:"line"`
}

// GuardCronList 读取系统 crontab
func GuardCronList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	entries, err := parseCrontab()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"entries": []CronEntry{}, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

type cronAction struct {
	Action   string    `json:"action"`
	Entry    CronEntry `json:"entry"`
}

// GuardCronAction 添加/删除/启用/禁用 crontab 条目
func GuardCronAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body cronAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid body"})
		return
	}

	switch body.Action {
	case "add":
		line := fmt.Sprintf("%s %s %s", body.Entry.Schedule, body.Entry.Command, body.Entry.Comment)
		cmd := exec.Command("crontab", "-l")
		existing, _ := cmd.Output()
		newCrontab := string(existing) + "\n" + line + "\n"
		writeCmd := exec.Command("sh", "-c", "echo "+quoteShell(newCrontab)+" | crontab -")
		if err := writeCmd.Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})

	case "delete":
		entries, _ := parseCrontab()
		var newLines []string
		for _, e := range entries {
			if e.ID != body.Entry.ID {
				newLines = append(newLines, fmt.Sprintf("%s %s %s", e.Schedule, e.Command, e.Comment))
			}
		}
		newCrontab := strings.Join(newLines, "\n") + "\n"
		if len(newLines) == 0 {
			newCrontab = ""
		}
		cmd := exec.Command("sh", "-c", "echo "+quoteShell(newCrontab)+" | crontab -")
		if err := cmd.Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})

	case "toggle":
		entries, _ := parseCrontab()
		var newLines []string
		for _, e := range entries {
			if e.ID == body.Entry.ID {
				if e.Enabled {
					// 注释掉
					newLines = append(newLines, "# "+fmt.Sprintf("%s %s %s", e.Schedule, e.Command, e.Comment))
				} else {
					newLines = append(newLines, fmt.Sprintf("%s %s %s", e.Schedule, e.Command, e.Comment))
				}
			} else {
				newLines = append(newLines, fmt.Sprintf("%s %s %s", e.Schedule, e.Command, e.Comment))
			}
		}
		newCrontab := strings.Join(newLines, "\n") + "\n"
		cmd := exec.Command("sh", "-c", "echo "+quoteShell(newCrontab)+" | crontab -")
		if err := cmd.Run(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})

	default:
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}

var cronRe = regexp.MustCompile(`^((?:@\w+|\S+\s+\S+\s+\S+\s+\S+\s+\S+))\s+(.+?)(?:\s*#\s*(.+))?$`)

func parseCrontab() ([]CronEntry, error) {
	cmd := exec.Command("crontab", "-l")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("无法读取 crontab: %v", err)
	}
	var entries []CronEntry
	lines := strings.Split(string(out), "\n")
	lineNum := 0
	for _, line := range lines {
		lineNum++
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 去掉被注释掉的
		enabled := true
		cleanLine := line
		if strings.HasPrefix(line, "# ") {
			enabled = false
			cleanLine = line[2:]
		}

		matches := cronRe.FindStringSubmatch(cleanLine)
		if matches == nil {
			continue
		}
		id := fmt.Sprintf("cron_%d", lineNum)
		comment := ""
		if len(matches) > 3 {
			comment = matches[3]
		}
		entries = append(entries, CronEntry{
			ID:       id,
			Schedule: matches[1],
			Command:  matches[2],
			Comment:  comment,
			Enabled:  enabled,
			Line:     lineNum,
		})
	}
	return entries, nil
}

func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
