-- +goose Up
-- CM-00012: repeat_results was stored as 0 before repeats were counted. Rows written from now on say so, and a row
-- from before stays unmeasured until its day is scanned again; nothing here rewrites stored rows.
ALTER TABLE tool_result_daily ADD COLUMN repeat_measured boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE tool_result_daily DROP COLUMN repeat_measured;
