package migration

import (
	"context"
	"testing"
	"testing/fstest"
)

func TestFileSystemLoader(t *testing.T) {
	// 构造虚拟文件系统
	fsys := fstest.MapFS{
		"20261102001_init.up.sql":   {Data: []byte("CREATE TABLE test1;")},
		"20261102001_init.down.sql": {Data: []byte("DROP TABLE test1;")},
		"20261102002_add.up.sql":    {Data: []byte("ALTER TABLE test1 ADD COLUMN val INT;")},
	}

	loader := NewFileSystemLoader(fsys, ".")
	scripts, err := loader.LoadScripts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(scripts) != 2 {
		t.Fatalf("expected 2 scripts, got %d", len(scripts))
	}

	// 校验排序是否正确（按版本号升序）
	if scripts[0].Version != "20261102001" || scripts[1].Version != "20261102002" {
		t.Errorf("scripts are not sorted correctly by version")
	}

	if scripts[0].UpSQL != "CREATE TABLE test1;" || scripts[0].DownSQL != "DROP TABLE test1;" {
		t.Errorf("script content parsed incorrectly")
	}

	// 缺少 down 脚本时，Up 正常解析且 DownSQL 为空
	if scripts[1].DownSQL != "" {
		t.Errorf("expected empty DownSQL for script without down file")
	}
}

// 边界：不符合 {version}_{description}.up/.down.sql 命名规范的文件应被静默忽略
func TestFileSystemLoaderIgnoresUnconventionalFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"readme.txt":      {Data: []byte("# docs")},    // 非 SQL 文件
		"notes.sql":       {Data: []byte("SELECT 1;")}, // 缺少 .up/.down 双后缀
		"nounder.up.sql":  {Data: []byte("SELECT 1;")}, // 版本号与描述间无下划线
		"20261102.up.sql": {Data: []byte("SELECT 1;")}, // 同上：baseInfo 无下划线
	}

	loader := NewFileSystemLoader(fsys, ".")
	scripts, err := loader.LoadScripts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scripts) != 0 {
		t.Errorf("expected unconventional files to be ignored, got %+v", scripts)
	}
}

// 边界：存在但为空的目录不报错，返回空列表
func TestFileSystemLoaderEmptyDir(t *testing.T) {
	loader := NewFileSystemLoader(fstest.MapFS{}, ".")
	scripts, err := loader.LoadScripts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scripts) != 0 {
		t.Errorf("expected no scripts from empty dir, got %d", len(scripts))
	}
}

// 边界：目录不存在时应显式报错（防止路径拼写错误被当作“无迁移”静默放行）
func TestFileSystemLoaderNonexistentDir(t *testing.T) {
	loader := NewFileSystemLoader(fstest.MapFS{}, "migrations")
	_, err := loader.LoadScripts(context.Background())
	if err == nil {
		t.Fatal("expected error for nonexistent migrations dir, got nil")
	}
}
