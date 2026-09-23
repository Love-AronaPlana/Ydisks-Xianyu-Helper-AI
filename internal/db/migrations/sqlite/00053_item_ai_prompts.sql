-- +goose Up
CREATE TABLE item_ai_prompts (
    cookie_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    strategy TEXT NOT NULL DEFAULT 'inherit',
    prompt TEXT NOT NULL DEFAULT '',
    custom_variables TEXT NOT NULL DEFAULT '{}',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(cookie_id, item_id),
    FOREIGN KEY(cookie_id, item_id) REFERENCES item_info(cookie_id, item_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE item_ai_prompts;
