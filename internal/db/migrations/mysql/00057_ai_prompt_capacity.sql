-- +goose Up
-- 提示词长度不再设上限：MySQL 的 TEXT 只能存 64KB，改用 LONGTEXT 让存储层不再成为限制。
ALTER TABLE ai_reply_settings
    MODIFY COLUMN ai_full_prompt LONGTEXT NULL,
    MODIFY COLUMN custom_prompts LONGTEXT NULL;

-- +goose Down
ALTER TABLE ai_reply_settings
    MODIFY COLUMN ai_full_prompt TEXT NULL,
    MODIFY COLUMN custom_prompts TEXT NULL;
