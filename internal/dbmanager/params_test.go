// 参数化查询(P1-9 方案 A)的语义测试。
//
// 这一组断言防的是: 占位符替换把**不该动的东西**动了(用户变量 / PG 类型转换 / 字符串里的冒号),
// 或者**该报错的没报**(对不上的占位符、没用上的参数、非法数字) —— 后者更危险:
// 它会让用户以为参数生效了, 实际执行的是别的语句。
package dbmanager

import (
	"strings"
	"testing"

	syncpkg "opscore/internal/dbmanager/sync"
)

func sp(name, typ, val string) SQLParam { return SQLParam{Name: name, Type: typ, Value: val} }

func TestSubstituteParamsBasic(t *testing.T) {
	got, err := SubstituteParams("SELECT * FROM t WHERE id = :id AND name = :nm",
		[]SQLParam{sp("id", "number", "42"), sp("nm", "string", "alice")}, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SELECT * FROM t WHERE id = 42 AND name = 'alice'" {
		t.Errorf("got %q", got)
	}
}

// 没有参数时整段不重写: 用户没用参数功能, SQL 里的冒号是合法的(JSON 路径等)。
func TestSubstituteNoParamsPassesThrough(t *testing.T) {
	in := "SELECT * FROM t WHERE j->>':key' = 'x'"
	got, err := SubstituteParams(in, nil, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("无参数时不该改动 SQL: %q", got)
	}
}

// 字符串字面量 / 注释里的 :name 不动 —— 否则会把数据改掉。
func TestSubstituteSkipsStringsAndComments(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT ':notparam' AS a", "SELECT ':notparam' AS a"},
		{"SELECT 1 -- :notparam\n", "SELECT 1 -- :notparam\n"},
		{"SELECT /* :notparam */ 1", "SELECT /* :notparam */ 1"},
		{"SELECT `:notparam` FROM t", "SELECT `:notparam` FROM t"},
	}
	for _, c := range cases {
		got, err := SubstituteParams(c.in, []SQLParam{sp("other", "number", "1")}, syncpkg.DialectMySQL)
		// 这里给了参数 other 但 SQL 里没有它 → 应当报"没用上"
		if err == nil {
			t.Errorf("%q: 期望报 参数没用到, 得到 %q", c.in, got)
			continue
		}
		if !strings.Contains(err.Error(), "没用到") {
			t.Errorf("%q: 错误信息应说清参数没用到, 得到 %v", c.in, err)
		}
	}
}

// PG 的 :: 强制类型转换必须跳过, 不能当成占位符。
func TestSubstituteSkipsPostgresCast(t *testing.T) {
	got, err := SubstituteParams("SELECT :v::text", []SQLParam{sp("v", "number", "7")}, syncpkg.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SELECT 7::text" {
		t.Errorf("got %q", got)
	}
}

// dollar-quote 里的内容不动(PG 函数体常写 $$ ... :x ... $$)。
func TestSubstituteSkipsDollarQuote(t *testing.T) {
	in := "SELECT $$ :notparam $$ AS body"
	got, err := SubstituteParams(in, []SQLParam{sp("v", "number", "1")}, syncpkg.DialectPostgres)
	if err == nil || !strings.Contains(err.Error(), "没用到") {
		t.Errorf("dollar-quote 里的占位符不该被替换: got=%q err=%v", got, err)
	}
}

// 对不上的占位符必须报错(fail closed), 而不是留给数据库报一句看不懂的错。
func TestSubstituteUnknownPlaceholderFails(t *testing.T) {
	_, err := SubstituteParams("SELECT * FROM t WHERE a = :missing", []SQLParam{sp("other", "number", "1")}, syncpkg.DialectMySQL)
	if err == nil {
		t.Fatal("缺少参数时应报错")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("错误信息应点名缺哪个参数: %v", err)
	}
}

// 给了但没用上的参数也要报 —— 静默忽略会让人以为参数生效了。
func TestSubstituteUnusedParamFails(t *testing.T) {
	_, err := SubstituteParams("SELECT 1", []SQLParam{sp("unused", "number", "1")}, syncpkg.DialectMySQL)
	if err == nil || !strings.Contains(err.Error(), "没用到") {
		t.Fatalf("应报参数没用到: %v", err)
	}
}

// number 类型只放行真数字: 这是唯一会"裸发"到 SQL 里的类型, 绝不能塞进其它内容。
func TestSubstituteNumberRejectsHostileValue(t *testing.T) {
	for _, bad := range []string{"1; DROP TABLE t", "1 OR 1=1", "abc", "1'--", "0x10"} {
		if _, err := SubstituteParams("SELECT :v", []SQLParam{sp("v", "number", bad)}, syncpkg.DialectMySQL); err == nil {
			t.Errorf("number 类型应拒绝 %q", bad)
		}
	}
	// 正常数字要放行(含负数/小数/大整数)
	for _, ok := range []string{"1", "-2", "3.14", "9007199254740993"} {
		if _, err := SubstituteParams("SELECT :v", []SQLParam{sp("v", "number", ok)}, syncpkg.DialectMySQL); err != nil {
			t.Errorf("number 类型应接受 %q: %v", ok, err)
		}
	}
}

// string 类型走方言转义: MySQL 反斜杠转义、PG 双写单引号。
func TestSubstituteStringEscapingPerDialect(t *testing.T) {
	my, err := SubstituteParams("SELECT :v", []SQLParam{sp("v", "string", "a'b\\c")}, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(my, "\\'") {
		t.Errorf("MySQL 应反斜杠转义单引号: %q", my)
	}
	pg, err := SubstituteParams("SELECT :v", []SQLParam{sp("v", "string", "a'b")}, syncpkg.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pg, "a''b") {
		t.Errorf("PG 应双写单引号: %q", pg)
	}
}

// 注入尝试: string 值里塞 SQL, 必须被转义成字面量而不是执行。
func TestSubstituteStringInjectionIsEscaped(t *testing.T) {
	got, err := SubstituteParams("SELECT * FROM t WHERE n = :v",
		[]SQLParam{sp("v", "string", "x' OR '1'='1")}, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	// 注入串必须整段落在引号里
	if !strings.Contains(got, "\\'") {
		t.Errorf("单引号未被转义: %q", got)
	}
}

// bool / null / raw 三种类型
func TestSubstituteBoolNullRaw(t *testing.T) {
	got, err := SubstituteParams("SELECT :a, :b, :c",
		[]SQLParam{sp("a", "bool", "true"), sp("b", "null", ""), sp("c", "raw", "1,2,3")}, syncpkg.DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SELECT TRUE, NULL, 1,2,3" {
		t.Errorf("got %q", got)
	}
}

// 大小写不敏感 + 同名参数多次出现都要替换
func TestSubstituteCaseInsensitiveAndRepeated(t *testing.T) {
	got, err := SubstituteParams("SELECT :ID, :id", []SQLParam{sp("Id", "number", "5")}, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SELECT 5, 5" {
		t.Errorf("got %q", got)
	}
}

// 未知类型要报错, 不猜
func TestSubstituteUnknownTypeFails(t *testing.T) {
	if _, err := SubstituteParams("SELECT :v", []SQLParam{sp("v", "weird", "1")}, syncpkg.DialectMySQL); err == nil {
		t.Fatal("未知参数类型应报错")
	}
}

// 替换后的 SQL 必须能被风险判定正确识别为写操作 —— 这是"替换在判定之前"的意义。
func TestSubstitutedSQLIsClassifiedCorrectly(t *testing.T) {
	got, err := SubstituteParams("DELETE FROM t WHERE id = :id", []SQLParam{sp("id", "number", "1")}, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := classifySQLRisk("mysql", got); r == RiskSafe {
		t.Error("替换后的 DELETE 必须被判成写操作")
	}
	// 只读语句仍是只读
	ro, err := SubstituteParams("SELECT * FROM t WHERE id = :id", []SQLParam{sp("id", "number", "1")}, syncpkg.DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := classifySQLRisk("mysql", ro); r != RiskSafe {
		t.Errorf("替换后的 SELECT 应仍为只读, 得到 %v", r)
	}
}
