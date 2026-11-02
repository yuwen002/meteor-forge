-- 为用户表增加邮箱字段与索引
ALTER TABLE users ADD COLUMN email VARCHAR(128);
CREATE INDEX idx_users_email ON users (email);
