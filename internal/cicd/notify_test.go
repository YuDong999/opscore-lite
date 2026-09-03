package cicd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNextCronFire(t *testing.T) {
	e := newTestEngine(t)
	// 每天 03:00 → 下次应为明天 03:00(或今天, 取决于当前时刻)
	p := &Pipeline{Name: "cron-next", Trigger: Trigger{Manual: true, Cron: "0 3 * * *"}, Stages: []Stage{
		{Name: "a", Steps: []Step{{Name: "s", Command: "echo x"}}},
	}}
	if err := e.SavePipeline(p); err != nil {
		t.Fatalf("SavePipeline: %v", err)
	}
	next := e.NextCronFire(p.ID)
	if next.IsZero() {
		t.Fatal("有 cron 的流水线应返回下次触发时间")
	}
	if next.Before(time.Now()) {
		t.Errorf("下次触发时间不应在过去: %v", next)
	}
	if next.Hour() != 3 || next.Minute() != 0 {
		t.Errorf("下次触发应为 03:00, 实际 %s", next.Format("15:04"))
	}
	// 无 cron → 零值
	p2 := &Pipeline{Name: "no-cron", Trigger: Trigger{Manual: true}, Stages: p.Stages}
	e.SavePipeline(p2)
	if !e.NextCronFire(p2.ID).IsZero() {
		t.Error("无 cron 应返回零值")
	}
}

func TestBuildNotifyBody(t *testing.T) {
	run := &Run{Pipeline: "发布", Status: StatusFailed, Trigger: "webhook", DurationMs: 91234, Error: "阶段 \"部署\" 失败"}
	// 钉钉: markdown 消息
	var ding struct {
		MsgType string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(buildNotifyBody(run, "dingtalk"), &ding); err != nil {
		t.Fatalf("dingtalk body: %v", err)
	}
	if ding.MsgType != "markdown" || !strings.Contains(ding.Markdown.Text, "发布") || !strings.Contains(ding.Markdown.Text, "失败原因") {
		t.Errorf("钉钉消息不符合预期: %+v", ding)
	}
	// 飞书: text 消息
	var fei struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(buildNotifyBody(run, "feishu"), &fei); err != nil {
		t.Fatalf("feishu body: %v", err)
	}
	if fei.MsgType != "text" || !strings.Contains(fei.Content.Text, "已取消") && !strings.Contains(fei.Content.Text, "失败") {
		t.Errorf("飞书消息不符合预期: %+v", fei)
	}
	// 通用: 含 runId
	var generic map[string]any
	if err := json.Unmarshal(buildNotifyBody(run, ""), &generic); err != nil {
		t.Fatalf("generic body: %v", err)
	}
	if generic["status"] != StatusFailed || generic["pipeline"] != "发布" {
		t.Errorf("通用消息不符合预期: %v", generic)
	}
}

func TestImportPipelineRenameOnConflict(t *testing.T) {
	e := newTestEngine(t)
	stages := []Stage{{Name: "a", Steps: []Step{{Name: "s", Command: "echo x"}}}}
	e.SavePipeline(&Pipeline{Name: "dup", Trigger: Trigger{Manual: true}, Stages: stages})
	e.SavePipeline(&Pipeline{Name: "other", Trigger: Trigger{Manual: true}, Stages: stages})

	imported, skipped, err := e.ImportPipeline([]Pipeline{
		{Name: "dup", Trigger: Trigger{Manual: true, Webhook: true, Secret: "SHOULD-RESET"}, Stages: stages},
		{Name: "fresh", Trigger: Trigger{Manual: true}, Stages: stages},
		{Name: "", Trigger: Trigger{Manual: true}, Stages: stages}, // 无名 → 跳过
	})
	if err != nil {
		t.Fatalf("ImportPipeline: %v", err)
	}
	if imported != 2 || skipped != 1 {
		t.Fatalf("导入 2 跳过 1, 实际导入 %d 跳过 %d", imported, skipped)
	}
	// 冲突名加了后缀; 导入后 webhook secret 被重置
	found := map[string]*Pipeline{}
	for _, p := range e.ListPipelines() {
		found[p.Name] = &p
	}
	if _, ok := found["dup-2"]; !ok {
		t.Errorf("冲突名应重命名为 dup-2, 现有: %v", keysOf(found))
	}
	if _, ok := found["fresh"]; !ok {
		t.Error("新名应原样导入")
	}
	full, _ := e.GetPipeline(found["dup-2"].ID)
	if full.Trigger.Secret == "SHOULD-RESET" {
		t.Error("导入的 webhook 凭证必须被重置")
	}
}

func keysOf(m map[string]*Pipeline) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
