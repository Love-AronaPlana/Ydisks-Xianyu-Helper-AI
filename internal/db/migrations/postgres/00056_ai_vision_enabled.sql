-- +goose Up
ALTER TABLE ai_reply_settings
    ADD COLUMN ai_vision_enabled BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE ai_reply_settings DROP COLUMN ai_vision_enabled;
