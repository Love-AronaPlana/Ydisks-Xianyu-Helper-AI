-- +goose Up
ALTER TABLE ai_reply_settings
    ADD COLUMN human_handoff_minutes INTEGER NOT NULL DEFAULT 0;

CREATE TABLE ai_human_handoffs (
    cookie_id VARCHAR(255) NOT NULL,
    buyer_id VARCHAR(255) NOT NULL,
    paused_until BIGINT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (cookie_id, buyer_id),
    CONSTRAINT fk_ai_human_handoffs_cookie FOREIGN KEY (cookie_id) REFERENCES cookies(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_ai_human_handoffs_paused_until
    ON ai_human_handoffs(cookie_id, paused_until);

-- +goose Down
DROP INDEX idx_ai_human_handoffs_paused_until ON ai_human_handoffs;
DROP TABLE ai_human_handoffs;
ALTER TABLE ai_reply_settings DROP COLUMN human_handoff_minutes;
