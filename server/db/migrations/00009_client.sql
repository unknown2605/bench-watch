-- +goose Up
-- CM-00016: usage, session and context-band days are kept per coding client; rows stored before are Claude Code's.
-- Keep the enum in step with server/internal/clients (store test TestClientEnumMatchesClients).
CREATE TYPE client_id AS ENUM ('claude-code', 'codex', 'opencode', 'gemini-cli', 'qwen-code', 'copilot-cli');

CREATE OR REPLACE FUNCTION is_model_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN length(v) <= 60 AND v ~ '^[a-z0-9]{1,24}(?:[._:-][a-z0-9]{1,24}){0,11}(?:\[[0-9]{1,2}[a-z]\])?$';

ALTER TABLE usage_daily ADD COLUMN client client_id NOT NULL DEFAULT 'claude-code';
ALTER TABLE usage_daily DROP CONSTRAINT usage_daily_pkey, ADD PRIMARY KEY (day, project, scope, model, client);

ALTER TABLE context_band_daily ADD COLUMN client client_id NOT NULL DEFAULT 'claude-code';
ALTER TABLE context_band_daily DROP CONSTRAINT context_band_daily_pkey,
    ADD PRIMARY KEY (day, project, scope, min_tokens, client);

ALTER TABLE session_daily ADD COLUMN client client_id NOT NULL DEFAULT 'claude-code';
ALTER TABLE session_daily DROP CONSTRAINT session_daily_pkey,
    ADD PRIMARY KEY (day, project, session, scope, model, client);

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    old_model constant text := '^(?:claude-[a-z0-9]{1,16}(?:-[a-z0-9]{1,16}){1,5}(?:\[[0-9]{1,2}[a-z]\])?|unknown)$';
    t text;
    n bigint;
BEGIN
    FOREACH t IN ARRAY ARRAY['usage_daily', 'context_band_daily', 'session_daily'] LOOP
        EXECUTE format('SELECT count(*) FROM public.%I WHERE client <> %L', t, 'claude-code') INTO n;
        IF n > 0 THEN
            RAISE EXCEPTION '00009 down: % holds % rows of clients other than claude-code; remove them first', t, n;
        END IF;
    END LOOP;
    FOREACH t IN ARRAY ARRAY['usage_daily', 'session_daily', 'subagent_daily'] LOOP
        EXECUTE format('SELECT count(*) FROM public.%I WHERE length(model) > 60 OR model !~ %L', t, old_model) INTO n;
        IF n > 0 THEN
            RAISE EXCEPTION '00009 down: % holds % rows whose model fails the 00001 label check', t, n;
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd

ALTER TABLE session_daily DROP CONSTRAINT session_daily_pkey, ADD PRIMARY KEY (day, project, session, scope, model);
ALTER TABLE session_daily DROP COLUMN client;

ALTER TABLE context_band_daily DROP CONSTRAINT context_band_daily_pkey,
    ADD PRIMARY KEY (day, project, scope, min_tokens);
ALTER TABLE context_band_daily DROP COLUMN client;

ALTER TABLE usage_daily DROP CONSTRAINT usage_daily_pkey, ADD PRIMARY KEY (day, project, scope, model);
ALTER TABLE usage_daily DROP COLUMN client;

CREATE OR REPLACE FUNCTION is_model_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN length(v) <= 60 AND v ~ '^(?:claude-[a-z0-9]{1,16}(?:-[a-z0-9]{1,16}){1,5}(?:\[[0-9]{1,2}[a-z]\])?|unknown)$';

DROP TYPE client_id;
