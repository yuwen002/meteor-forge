package migration

import "testing"

func TestScriptChecksumProperties(t *testing.T) {
	a := &Script{UpSQL: "CREATE TABLE t;", DownSQL: "DROP TABLE t;"}
	b := &Script{UpSQL: "CREATE TABLE t;", DownSQL: "DROP TABLE t;"}

	// 相同内容 => 相同摘要（可跨进程/跨机器复现）
	if a.Checksum() != b.Checksum() {
		t.Error("identical content must produce identical checksum")
	}

	// 内容变化 => 摘要变化（up 或 down 任一侧）
	c := &Script{UpSQL: "CREATE TABLE t2;", DownSQL: "DROP TABLE t;"}
	if a.Checksum() == c.Checksum() {
		t.Error("changed UpSQL must produce different checksum")
	}
	d := &Script{UpSQL: "CREATE TABLE t;", DownSQL: "DROP TABLE t2;"}
	if a.Checksum() == d.Checksum() {
		t.Error("changed DownSQL must produce different checksum")
	}

	// 摘要不依赖 Version/Description：重命名文件描述不误报篡改
	e := &Script{Version: "999", Description: "renamed", UpSQL: a.UpSQL, DownSQL: a.DownSQL}
	if a.Checksum() != e.Checksum() {
		t.Error("checksum should ignore version/description metadata")
	}

	// 拼接歧义防护："ab"+"c" 与 "a"+"bc" 不得产生相同摘要
	x := &Script{UpSQL: "ab", DownSQL: "c"}
	y := &Script{UpSQL: "a", DownSQL: "bc"}
	if x.Checksum() == y.Checksum() {
		t.Error("NUL separator failed to disambiguate up/down boundary")
	}

	// 行尾归一化：CRLF 与 LF 仅行尾不同，不得被判为篡改（git autocrlf 场景）
	lf := &Script{UpSQL: "CREATE TABLE t;\n", DownSQL: "DROP TABLE t;\n"}
	crlf := &Script{UpSQL: "CREATE TABLE t;\r\n", DownSQL: "DROP TABLE t;\r\n"}
	if lf.Checksum() != crlf.Checksum() {
		t.Error("line-ending-only differences must not change checksum")
	}
}
