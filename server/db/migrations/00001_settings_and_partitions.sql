-- +goose Up
CREATE TYPE retention_tier AS ENUM ('raw', 'rollup_1m', 'rollup_1h', 'events');
CREATE TYPE partition_period AS ENUM ('day', 'month');
CREATE TYPE token_band_kind AS ENUM ('context', 'result_size');

-- Defaults decided by the user on 2026-11-08 (implementation_plan.md, Data and retention). A NULL keep means the
-- tier is kept forever: drop_expired_partitions never drops it.
CREATE TABLE retention_settings (
    tier retention_tier PRIMARY KEY,
    keep interval CHECK (keep IS NULL OR keep >= interval '1 day'),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO retention_settings (tier, keep) VALUES
    ('raw', interval '14 days'),
    ('rollup_1m', interval '2 years'),
    ('rollup_1h', NULL),
    ('events', interval '2 years');

-- drop_expired_partitions rolls an expiring raw day or minute month into the next tier, so that tier must be kept
-- at least as long; checked at commit, so one transaction can move several tiers.
-- +goose StatementBegin
CREATE FUNCTION check_retention_order()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    raw_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'raw'), 'infinity');
    minute_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1m'), 'infinity');
    hour_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1h'), 'infinity');
BEGIN
    IF NOT (raw_keep <= minute_keep AND minute_keep <= hour_keep) THEN
        RAISE EXCEPTION 'retention must keep raw <= rollup_1m <= rollup_1h' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER retention_order AFTER INSERT OR UPDATE ON retention_settings
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_retention_order();

CREATE TABLE partition_policy (
    parent text PRIMARY KEY CHECK (parent ~ '^[a-z][a-z0-9_]{0,40}$'),
    period partition_period NOT NULL,
    tier retention_tier NOT NULL REFERENCES retention_settings (tier)
);

-- The bands the scanner uses now. Daily tables store each row's own bounds, so editing these never rewrites history.
CREATE TABLE token_bands (
    kind token_band_kind NOT NULL,
    min_tokens bigint NOT NULL CHECK (min_tokens >= 0),
    max_tokens bigint CHECK (max_tokens IS NULL OR max_tokens > min_tokens),
    PRIMARY KEY (kind, min_tokens),
    UNIQUE NULLS NOT DISTINCT (kind, max_tokens)
);
-- Context bounds are the Node bandOf (lib/aggregate.mjs at 4ab56da) (0-100k ... 700k+), decided 2026-11-08.
INSERT INTO token_bands (kind, min_tokens, max_tokens) VALUES
    ('context', 0, 100000),
    ('context', 100000, 200000),
    ('context', 200000, 400000),
    ('context', 400000, 700000),
    ('context', 700000, NULL),
    ('result_size', 0, 1000),
    ('result_size', 1000, 5000),
    ('result_size', 5000, 20000),
    ('result_size', 20000, 50000),
    ('result_size', 50000, NULL);

CREATE TYPE display_setting_name AS ENUM ('reread_target_share');
CREATE TABLE display_settings (
    name display_setting_name PRIMARY KEY,
    value double precision NOT NULL CHECK (value >= 0 AND value <= 1),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO display_settings (name, value) VALUES ('reread_target_share', 0.15);

-- Each pattern equals its schema's in api/openapi.yaml (internal/api TestDatabaseChecksMatchTheSpec), so a value
-- that skipped the gate cannot be stored. The sentinels the scanners emit are part of each pattern.
CREATE FUNCTION is_project_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^(?:[A-Za-z0-9][A-Za-z0-9._-]{0,119}|scratch \(tmp\))$';
CREATE FUNCTION is_model_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN length(v) <= 60 AND v ~ '^(?:claude-[a-z0-9]{1,16}(?:-[a-z0-9]{1,16}){1,5}(?:\[[0-9]{1,2}[a-z]\])?|unknown)$';
CREATE FUNCTION is_agent_type_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[A-Za-z][A-Za-z0-9._:-]{0,59}$';
CREATE FUNCTION is_tool_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN length(v) <= 125 AND v ~ '^(?:[A-Za-z][A-Za-z0-9_-]{0,59}|Read: (?:whole|ranged|image)|Bash: (?:artisan|awk|aws|az|base64|basename|bash|bc|biome|brew|bun|bundle|cargo|cat|cd|chmod|chown|cloudflared|cmake|cmp|column|comm|command|composer|convert|cp|curl|cut|date|defaults|deno|df|diff|dig|dirname|dnode|docker|docker-compose|dphp|dredis|dsql|du|echo|egrep|env|eslint|exiftool|expr|false|fd|ffmpeg|ffprobe|file|find|gcloud|gem|gh|git|gitleaks|go|gofmt|gofumpt|golangci-lint|govulncheck|grep|gunzip|gzip|head|helm|hexdump|hostname|http|id|identify|jest|jq|kill|kubectl|launchctl|less|ln|ls|lsof|magento-cloud|magick|make|mariadb|md5|md5sum|mkdir|mv|mypy|mysql|mysqldump|nc|netstat|nginx|nl|node|npm|npx|nvm|od|open|openssl|osascript|other|paste|pbcopy|pbpaste|perl|pg_dump|pgrep|php|ping|pip|pip3|pkill|playwright|plutil|pm2|pnpm|prettier|printf|ps|psql|pwd|pytest|python|python3|readlink|realpath|redis-cli|rg|rm|rmdir|rsync|ruby|ruff|rustc|scp|script|sed|semgrep|seq|serverless|sftp|sh|sha1sum|sha256sum|shasum|shell loop|shopify|sips|sleep|sls|sort|sqlite3|ssh|stack-doctor|stat|staticcheck|strings|sw_vers|sysctl|tail|tar|tee|terraform|test|top|touch|tr|trivy|true|tsc|tsx|type|uname|uniq|unzip|uv|uvx|vitest|vm_stat|wait|wc|wget|which|whoami|wrangler|xargs|xxd|yarn|yes|yq|zip|zsh)|mcp__[A-Za-z0-9_-]{1,60}__[A-Za-z0-9_-]{1,60})$';
CREATE FUNCTION is_session_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^(?:[0-9a-f]{8}|unknown)$';
CREATE FUNCTION is_hash16(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[0-9a-f]{16}$';
-- A boundary with no record UUID is keyed by its clamped Unix time in decimal (extract.mjs, extract.go).
CREATE FUNCTION is_boundary_key(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^(?:[0-9a-f]{16}|[1-9][0-9]{8,12})$';
CREATE FUNCTION is_container_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$';
CREATE FUNCTION is_stack_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[a-z0-9][a-z0-9_-]{0,62}$';
CREATE FUNCTION is_display_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[A-Za-z0-9][A-Za-z0-9 ._()+&-]{0,63}$';
CREATE FUNCTION is_filesystem_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[a-z0-9][a-z0-9_-]{0,15}$';

-- Partition bounds are UTC midnights (days) and UTC month starts, so DST never makes a gap or overlap.
-- Every partition function pins search_path with pg_temp last, so a temporary table cannot stand in for a partition.
-- +goose StatementBegin
CREATE FUNCTION ensure_partitions(at timestamptz DEFAULT now(), ahead integer DEFAULT 1)
RETURNS integer
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    p record;
    step interval;
    base timestamp;
    lo timestamp;
    child text;
    created integer := 0;
BEGIN
    IF ahead < 0 OR ahead > 62 THEN
        RAISE EXCEPTION 'ahead must be between 0 and 62';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('benchwatch.partitions'));
    FOR p IN SELECT parent, period FROM partition_policy ORDER BY parent LOOP
        step := CASE p.period WHEN 'day' THEN interval '1 day' ELSE interval '1 month' END;
        base := date_trunc(p.period::text, at AT TIME ZONE 'UTC');
        FOR i IN 0..ahead LOOP
            lo := base + step * i;
            child := p.parent || '_p' || to_char(lo, CASE p.period WHEN 'day' THEN 'YYYYMMDD' ELSE 'YYYYMM' END);
            IF to_regclass(format('public.%I', child)) IS NULL THEN
                EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
                    child, p.parent, lo AT TIME ZONE 'UTC', (lo + step) AT TIME ZONE 'UTC');
                created := created + 1;
            END IF;
        END LOOP;
    END LOOP;
    RETURN created;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION list_partitions()
RETURNS TABLE (parent text, child text, bounds text)
LANGUAGE sql STABLE
SET search_path = public, pg_temp
AS $$
    SELECT pc.relname::text, c.relname::text, pg_get_expr(c.relpartbound, c.oid)
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class pc ON pc.oid = i.inhparent
    JOIN partition_policy pp ON pp.parent = pc.relname
    WHERE pc.relnamespace = 'public'::regnamespace AND c.relnamespace = 'public'::regnamespace
    ORDER BY 1, 2
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION list_partitions();
DROP FUNCTION ensure_partitions(timestamptz, integer);
DROP FUNCTION is_filesystem_label(text);
DROP FUNCTION is_display_label(text);
DROP FUNCTION is_stack_label(text);
DROP FUNCTION is_container_label(text);
DROP FUNCTION is_boundary_key(text);
DROP FUNCTION is_hash16(text);
DROP FUNCTION is_session_label(text);
DROP FUNCTION is_tool_label(text);
DROP FUNCTION is_agent_type_label(text);
DROP FUNCTION is_model_label(text);
DROP FUNCTION is_project_label(text);
DROP TABLE display_settings;
DROP TYPE display_setting_name;
DROP TABLE token_bands;
DROP TABLE partition_policy;
DROP TABLE retention_settings;
DROP FUNCTION check_retention_order();
DROP TYPE token_band_kind;
DROP TYPE partition_period;
DROP TYPE retention_tier;
