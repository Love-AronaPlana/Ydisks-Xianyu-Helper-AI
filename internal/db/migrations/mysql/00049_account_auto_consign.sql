-- +goose Up
ALTER TABLE cookies ADD COLUMN auto_consign INT NOT NULL DEFAULT 0 AFTER auto_confirm;

-- +goose Down
ALTER TABLE cookies DROP COLUMN auto_consign;
