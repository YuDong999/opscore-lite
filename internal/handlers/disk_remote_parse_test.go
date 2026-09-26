package handlers

import "testing"

// 远程磁盘下钻的分段输出解析。这几条都是"错了也不报错、只是数字默默不对"的地方。
func TestParseRemoteDiskOutput(t *testing.T) {
	out := "__OPSCORE_DF__\n" +
		"/dev/sda1      38701056  31765760    6935296  83% /\n" +
		"__OPSCORE_DU__\n" +
		"30940800\t/\n" + // 目录自身那一行不该进子项
		"11520000\t/var/lib\n" +
		"2097152\t/var/log\n" +
		"__OPSCORE_DIRS__\n" +
		"/var/lib\n" +
		"/var/log\n" +
		"/var/empty\n" + // 只有目录、du 没给出尺寸(空目录) —— 仍要算目录
		"__OPSCORE_END__\n"

	dc, sizes, dirs := parseRemoteDiskOutput(out, "/")
	if dc.Total != 38701056*1024 || dc.Used != 31765760*1024 {
		t.Errorf("df 解析错: total=%d used=%d", dc.Total, dc.Used)
	}
	if _, ok := sizes["/"]; ok {
		t.Error("du 的\"目录自身\"行被当成了子项")
	}
	if sizes["/var/lib"] != 11520000*1024 {
		t.Errorf("KB 没换算成字节: %d", sizes["/var/lib"])
	}
	if !dirs["/var/empty"] {
		t.Error("dirs 段应独立于 du 生效")
	}
	if dc.Partial {
		t.Error("没有超时标记时不该报 partial")
	}
}

// du 被自我设限掐掉: 标记要转成 partial, 且它本身不能混成一条尺寸。
func TestParseRemoteDiskOutputPartialMarker(t *testing.T) {
	out := "__OPSCORE_DF__\n/dev/sda1 38701056 31765760 6935296 83% /\n" +
		"__OPSCORE_DU__\n11520000\t/var/lib\n__OPSCORE_DU_PARTIAL__\n" +
		"__OPSCORE_DIRS__\n/var/lib\n__OPSCORE_END__\n"

	dc, sizes, _ := parseRemoteDiskOutput(out, "/")
	if !dc.Partial {
		t.Fatal("du 超时标记没被识别, 界面会把局部当成全部")
	}
	if len(sizes) != 1 {
		t.Errorf("标记行被当成尺寸行解析了: %v", sizes)
	}
}

// 段名之外的杂行(权限报错、空行)不该进结果。
func TestParseRemoteDiskOutputIgnoresNoise(t *testing.T) {
	out := "du: cannot read directory '/x': Permission denied\n" +
		"__OPSCORE_DU__\n\n1024\t/a\n\t/b\n__OPSCORE_END__\n"
	dc, sizes, _ := parseRemoteDiskOutput(out, "/")
	if len(sizes) != 1 || sizes["/a"] != 1024*1024 {
		t.Errorf("噪声行或空尺寸行混进来了: %v", sizes)
	}
	if dc.Partial {
		t.Error("不该有 partial")
	}
}
