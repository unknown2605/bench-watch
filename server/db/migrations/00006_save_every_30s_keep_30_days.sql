-- +goose Up
-- CM-00013 (user decision 2026-11-11): Machine and Docker history saved every 30 s and kept at most 30 days.
-- Only the 10 s defaults move; an interval someone set by hand stays.
UPDATE collector_settings SET detail_interval_seconds = 30, updated_at = now() WHERE detail_interval_seconds = 10;
UPDATE collector_settings SET top_apps_interval_seconds = 30, updated_at = now() WHERE top_apps_interval_seconds = 10;

-- Up lets the next cleanup delete Machine and Docker history older than the new limits; Down does not restore it.
-- Only tiers still at the 00001 defaults move, and a rollup only when the tier below it stays within raw <= 1m <= 1h.
UPDATE retention_settings SET updated_at = now(), keep = CASE tier
    WHEN 'raw' THEN interval '7 days'
    ELSE interval '30 days'
END
WHERE (tier = 'raw' AND keep = interval '14 days')
    OR (tier = 'events' AND keep = interval '2 years')
    OR (tier = 'rollup_1m' AND keep = interval '2 years'
        AND (SELECT keep FROM retention_settings WHERE tier = 'raw') <= interval '30 days')
    OR (tier = 'rollup_1h' AND keep IS NULL
        AND ((SELECT keep FROM retention_settings WHERE tier = 'rollup_1m') <= interval '30 days'
            OR ((SELECT keep FROM retention_settings WHERE tier = 'rollup_1m') = interval '2 years'
                AND (SELECT keep FROM retention_settings WHERE tier = 'raw') <= interval '30 days')));

-- +goose Down
UPDATE collector_settings SET detail_interval_seconds = 10, updated_at = now() WHERE detail_interval_seconds = 30;
UPDATE collector_settings SET top_apps_interval_seconds = 10, updated_at = now() WHERE top_apps_interval_seconds = 30;

UPDATE retention_settings SET updated_at = now(), keep = CASE tier
    WHEN 'raw' THEN interval '14 days'
    WHEN 'rollup_1h' THEN NULL
    ELSE interval '2 years'
END
WHERE (tier = 'rollup_1h' AND keep = interval '30 days')
    OR (tier = 'events' AND keep = interval '30 days')
    OR (tier = 'rollup_1m' AND keep = interval '30 days'
        AND coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1h') = interval '30 days'
            OR (SELECT keep FROM retention_settings WHERE tier = 'rollup_1h') >= interval '2 years', true))
    OR (tier = 'raw' AND keep = interval '7 days'
        AND coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1m') >= interval '14 days', true));
