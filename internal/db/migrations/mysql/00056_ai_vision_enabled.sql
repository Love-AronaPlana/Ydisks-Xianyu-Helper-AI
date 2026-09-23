-- +goose Up
ALTER TABLE ai_reply_settings
    ADD COLUMN ai_vision_enabled TINYINT(1) NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE ai_reply_settings DROP COLUMN ai_vision_enabled;
