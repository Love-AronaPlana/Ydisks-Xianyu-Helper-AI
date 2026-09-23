-- +goose Up
CREATE TABLE item_ai_prompts (
    cookie_id VARCHAR(255) NOT NULL,
    item_id VARCHAR(255) NOT NULL,
    strategy VARCHAR(16) NOT NULL DEFAULT 'inherit',
    prompt TEXT NOT NULL,
    custom_variables JSON NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY(cookie_id, item_id),
    CONSTRAINT fk_item_ai_prompts_item FOREIGN KEY(cookie_id, item_id) REFERENCES item_info(cookie_id, item_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE item_ai_prompts;
