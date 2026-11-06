# 管理元数据库备份与恢复

管理元数据库与目标业务数据库分离，保存数据源注册、加密 DSN、租户映射、迁移/分片任务、管理员会话和审计记录。必须将它视为敏感的控制面数据进行备份和恢复。

## 在线备份

推荐使用 CLI 的 `metadata-backup` 子命令。该命令读取与 Web 服务相同的 `METEOR_ADMIN_DB` 和 `METEOR_ADMIN_ENCRYPTION_KEY` 配置：

```bash
export METEOR_ADMIN_DB=/var/lib/meteor-forge/admin.db
export METEOR_ADMIN_ENCRYPTION_KEY='<与服务相同的 64 位十六进制密钥>'

./meteor-forge metadata-backup \
  --to "/var/backups/meteor-forge/admin-$(date +%Y%m%d-%H%M%S).db"
```

如果未设置 `METEOR_ADMIN_DB`，默认路径为操作系统用户配置目录下的 `meteor-forge/admin.db`。

备份行为：

- 使用 SQLite `VACUUM INTO` 创建一致性快照，可在 Web 管理服务运行时执行；WAL 中已提交的数据会包含在快照中。
- 备份后执行 `PRAGMA integrity_check`，并验证存在 schema migration history。
- 备份文件权限设置为 `0600`，父目录权限为 `0700`。
- 不覆盖已存在的目标文件。每次备份使用新的文件名。
- 输出当前 schema version，便于记录备份点。

备份文件包含加密 DSN、管理员和审计等控制面数据。虽然 DSN 已加密，仍应限制备份文件访问并使用加密磁盘/备份介质。不要将备份文件或加密密钥提交到 Git。

## 恢复步骤

恢复必须在管理服务停止后进行，避免运行中的 SQLite 连接继续写入旧数据库。

1. 停止 meteor-forge Web 管理服务，并确认没有其他进程打开管理元数据库。
2. 备份当前数据库文件及其 `-wal`、`-shm` 伴随文件，以便恢复失败时回退。
3. 将待恢复备份复制到目标数据库同一文件系统下的临时文件。
4. 使用 SQLite 工具验证临时文件：

   ```bash
   sqlite3 /var/lib/meteor-forge/admin.db.restore 'PRAGMA integrity_check;'
   ```

   结果必须为 `ok`。
5. 设置文件权限为 `0600`，再原子替换 `METEOR_ADMIN_DB` 指向的数据库文件。
6. 只有在服务停止且旧数据库及 WAL/SHM 文件已妥善保留后，才清理与旧数据库关联的 `-wal`、`-shm` 文件，避免旧 WAL 被应用到恢复后的数据库。
7. 使用创建该备份时相同的 `METEOR_ADMIN_ENCRYPTION_KEY` 启动服务。启动时管理元数据库 schema 初始化/升级流程会补齐当前版本所需的结构。
8. 登录管理后台，核对数据源、租户、任务、管理员和审计记录；再对目标数据源执行连接测试。

示例中的数据库路径和服务管理命令应替换为实际部署值。恢复前不要删除原数据库和旧伴随文件；确认恢复完成并通过业务核对后，再按备份保留策略清理。

## 密钥与 schema 版本

- `METEOR_ADMIN_ENCRYPTION_KEY` 必须是 32 字节密钥的 64 位十六进制表示。丢失密钥后，备份中的 DSN 密文无法解密。
- 密钥应通过独立的密钥管理/安全配置渠道备份，不要与 SQLite 文件放在同一可公开访问的位置。
- `admin_schema_migrations` 保存管理元数据库已应用的 schema version。每次升级前建议先创建在线备份；升级失败时保留旧数据库文件和备份，先恢复后再排查。
- 备份只覆盖 meteor-forge 管理元数据库，不会备份 MySQL、PostgreSQL、SQLite 或 ClickHouse 目标业务数据库。目标库必须使用各自的数据库备份策略。
