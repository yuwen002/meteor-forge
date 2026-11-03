package migration

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// FileSystemLoader 基于标准 fs.FS（支持本地文件夹或 go:embed）的脚本加载器
type FileSystemLoader struct {
	fsys fs.FS
	root string
}

// NewFileSystemLoader 创建脚本加载器。root 为空时默认从 fsys 根目录 "." 开始扫描。
func NewFileSystemLoader(fsys fs.FS, root string) *FileSystemLoader {
	if root == "" {
		root = "."
	}
	return &FileSystemLoader{fsys: fsys, root: root}
}

// LoadScripts 扫描目录并解析 Up 和 Down 脚本。
// 文件命名规范: {version}_{description}.up.sql / {version}_{description}.down.sql
func (l *FileSystemLoader) LoadScripts(ctx context.Context) ([]*Script, error) {
	scriptMap := make(map[string]*Script)

	err := fs.WalkDir(l.fsys, l.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		filename := d.Name()
		// 校验后缀
		if !strings.HasSuffix(filename, ".sql") {
			return nil
		}

		// 解析文件名格式: {version}_{description}.up.sql / .down.sql
		parts := strings.SplitN(filename, ".", 3)
		if len(parts) < 3 {
			return nil
		}

		baseInfo := parts[0] // 例如 2026110201_create_users
		ext := parts[1]      // up 或 down

		splitBase := strings.SplitN(baseInfo, "_", 2)
		if len(splitBase) < 2 {
			return nil
		}
		version := splitBase[0]
		description := splitBase[1]

		script, exists := scriptMap[version]
		if !exists {
			script = &Script{
				Version:     version,
				Description: description,
			}
			scriptMap[version] = script
		}

		// 读取文件内容
		contentBytes, err := fs.ReadFile(l.fsys, path)
		if err != nil {
			return fmt.Errorf("read migration file %s failed: %w", path, err)
		}
		content := string(contentBytes)

		switch ext {
		case "up":
			script.UpSQL = content
		case "down":
			script.DownSQL = content
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// 转换为切片并按版本号排序
	var scripts []*Script
	for _, s := range scriptMap {
		scripts = append(scripts, s)
	}

	sort.Slice(scripts, func(i, j int) bool {
		return scripts[i].Version < scripts[j].Version
	})

	return scripts, nil
}