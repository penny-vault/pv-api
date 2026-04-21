-- 6_unofficial_strategies.down.sql

DROP INDEX IF EXISTS idx_strategies_clone_ver;

ALTER TABLE portfolios
    ADD CONSTRAINT portfolios_strategy_code_fkey
        FOREIGN KEY (strategy_code) REFERENCES strategies(short_code);

ALTER TABLE portfolios
    ALTER COLUMN strategy_ver SET NOT NULL;

ALTER TABLE portfolios
    DROP COLUMN strategy_clone_url,
    DROP COLUMN strategy_describe_json;
