-- +goose Up
ALTER TABLE ai_reply_settings
    ADD COLUMN human_handoff_minutes INTEGER NOT NULL DEFAULT 0;

CREATE TABLE ai_human_handoffs (
    cookie_id TEXT NOT NULL,
    buyer_id TEXT NOT NULL,
    paused_until INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (cookie_id, buyer_id),
    FOREIGN KEY (cookie_id) REFERENCES cookies(id) ON DELETE CASCADE
);

CREATE INDEX idx_ai_human_handoffs_paused_until
    ON ai_human_handoffs(cookie_id, paused_until);

-- +goose Down
DROP INDEX IF EXISTS idx_ai_human_handoffs_paused_until;
DROP TABLE IF EXISTS ai_human_handoffs;
ALTER TABLE ai_reply_settings DROP COLUMN human_handoff_minutes;
