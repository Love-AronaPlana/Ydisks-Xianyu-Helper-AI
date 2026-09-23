-- +goose Up
ALTER TABLE cookies ADD COLUMN captcha_browser_mode TEXT NOT NULL DEFAULT 'playwright';

-- +goose Down
ALTER TABLE cookies DROP COLUMN captcha_browser_mode;
