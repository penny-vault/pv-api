-- 6_unofficial_strategies.up.sql
-- Adds per-portfolio strategy clone URL + frozen describe JSON so unofficial
-- strategies can be used without a registry row. See
-- docs/superpowers/specs/2026-04-19-pvapi-3-0-unofficial-strategies.md.

ALTER TABLE portfolios
    ADD COLUMN strategy_clone_url     TEXT,
    ADD COLUMN strategy_describe_json JSONB;

UPDATE portfolios p
   SET strategy_clone_url    = s.clone_url,
       strategy_describe_json = s.describe_json
  FROM strategies s
 WHERE p.strategy_code = s.short_code;

ALTER TABLE portfolios
    ALTER COLUMN strategy_clone_url     SET NOT NULL,
    ALTER COLUMN strategy_describe_json SET NOT NULL,
    ALTER COLUMN strategy_ver           DROP NOT NULL,
    DROP CONSTRAINT portfolios_strategy_code_fkey;

CREATE INDEX idx_strategies_clone_ver
    ON strategies(clone_url, installed_ver)
 WHERE install_error IS NULL;
