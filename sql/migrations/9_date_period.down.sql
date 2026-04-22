-- sql/migrations/9_date_period.down.sql

DROP INDEX IF EXISTS idx_portfolios_due;

ALTER TABLE portfolios
    DROP COLUMN IF EXISTS start_date,
    DROP COLUMN IF EXISTS end_date;

CREATE TYPE portfolio_mode AS ENUM ('one_shot', 'continuous');

ALTER TABLE portfolios
    ADD COLUMN mode        portfolio_mode NOT NULL DEFAULT 'one_shot',
    ADD COLUMN schedule    TEXT,
    ADD COLUMN next_run_at TIMESTAMPTZ;

CREATE INDEX idx_portfolios_due ON portfolios (next_run_at)
    WHERE mode = 'continuous' AND status IN ('ready', 'failed');
