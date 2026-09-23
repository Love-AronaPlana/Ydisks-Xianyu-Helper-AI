-- +goose Up
ALTER TABLE ai_reply_settings ADD COLUMN ai_reply_mode TEXT NOT NULL DEFAULT 'bargain';
ALTER TABLE ai_reply_settings ADD COLUMN ai_full_prompt TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ai_reply_settings DROP COLUMN ai_full_prompt;
ALTER TABLE ai_reply_settings DROP COLUMN ai_reply_mode;
