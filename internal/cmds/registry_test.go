package cmds

import (
	"reflect"
	"testing"
)

func TestFind(t *testing.T) {
	if Find("tasks.crontab.list") == nil {
		t.Fatal("tasks.crontab.list 应存在于注册表")
	}
	if Find("no.such.cmd") != nil {
		t.Fatal("不存在的命令应返回 nil")
	}
}

func TestByModule(t *testing.T) {
	linux := ByModule("tasks", "linux")
	if len(linux) == 0 {
		t.Fatal("tasks/linux 组不应为空")
	}
	for _, c := range linux {
		if c.Module != "tasks" || c.Platform != "linux" {
			t.Fatalf("返回了错误组: %+v", c)
		}
	}
	win := ByModule("tasks", "win")
	if len(win) == 0 {
		t.Fatal("tasks/win 组不应为空")
	}
	if len(ByModule("no.such", "")) != 0 {
		t.Fatal("不存在的模块应返回空")
	}
}

func TestExpand(t *testing.T) {
	c := Find("tasks.crontab.list")
	if c == nil {
		t.Fatal("命令不存在")
	}
	got := Expand(c, nil)
	want := []string{"crontab", "-l"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand 结果错误: got %v want %v", got, want)
	}

	dummy := &Command{Args: []string{"echo", "{{user}}", "{{os}}"}}
	got = Expand(dummy, map[string]string{"user": "root"})
	want = []string{"echo", "root", "{{os}}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand 插值错误: got %v want %v", got, want)
	}
}

func TestRemoteWith(t *testing.T) {
	c := Find("tasks.crontab.write")
	if c == nil {
		t.Fatal("命令不存在")
	}
	if !c.Stdin {
		t.Fatal("tasks.crontab.write 应标记 Stdin")
	}
	if got := RemoteWith(c, nil); got != "crontab -" {
		t.Fatalf("Remote 模板错误: %s", got)
	}
	if got := RemoteWith(nil, nil); got != "" {
		t.Fatal("nil 命令应返回空")
	}
}
