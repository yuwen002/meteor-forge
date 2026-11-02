// Package migration 是 Schema 版本控制引擎：
// Loader 负责解析迁移脚本来源，Driver 抽象底层数据库操作，Migrator 对外提供
// Up/Down/DownSteps/ToVersion/Status 能力（ToVersion 即双向精准跳转，保留值 "0" 回滚全部）。
// 本包不引入任何具体数据库的第三方驱动包；MySQL/ClickHouse 等实现位于 pkg/driver/ 下并依赖本包。
package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var (
	ErrNoPendingMigrations = errors.New("migration: no pending migrations to run")
	ErrInvalidVersion      = errors.New("migration: invalid target version")
	// ErrChecksumMismatch 表示已应用过的脚本内容被篡改（本地哈希与库中存档不一致），
	// 继续执行可能导致代码与数据库结构对不上，引擎会中止迁移要求人工介入。
	ErrChecksumMismatch = errors.New("migration: checksum mismatch, script content has been tampered")
)

// Script 表示单个 SQL 迁移文件
type Script struct {
	Version     string // 示例: "20261102001" 或 "000001"
	Description string // 示例: "create_users_table"
	UpSQL       string // 向上迁移执行的 SQL
	DownSQL     string // 回滚时执行的 SQL
	CreatedAt   time.Time
}

// Checksum 计算脚本内容的 sha256 十六进制摘要（同时覆盖 up 与 down）。
// 计算前先做行尾归一化（CRLF/CR -> LF）：git autocrlf、编辑器 unix/dos 模式等
// 工具链噪声不应被判为内容篡改；SQL 语句本身的变更仍会被完整捕获。
// 以 NUL 字节分隔避免 "ab"+"c" 与 "a"+"bc" 产生相同摘要；
// 摘要不依赖 Version/Description，因此重命名文件描述不会触发篡改误报。
func (s *Script) Checksum() string {
	h := sha256.New()
	h.Write([]byte(normalizeLineEndings(s.UpSQL)))
	h.Write([]byte{0})
	h.Write([]byte(normalizeLineEndings(s.DownSQL)))
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeLineEndings 将 CRLF 与孤立 CR 统一为 LF，仅用于校验和计算，
// 不影响实际下发给数据库的 SQL。
func normalizeLineEndings(sql string) string {
	sql = strings.ReplaceAll(sql, "\r\n", "\n")
	return strings.ReplaceAll(sql, "\r", "\n")
}

// Record 表示迁移历史表中记录的一条数据
type Record struct {
	Version   string
	Name      string
	AppliedAt time.Time
	// Checksum 为应用时刻脚本内容的 sha256 摘要；
	// 空串表示旧版本工具写入的存量记录（无存档哈希），引擎会跳过比对以保持兼容。
	Checksum string
}

// StatusItem 迁移状态对比项
type StatusItem struct {
	Version     string
	Description string
	IsApplied   bool
}
