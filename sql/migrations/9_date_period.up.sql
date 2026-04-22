-- sql/migrations/9_date_period.up.sql

-- Drop index that references mode before altering the column.
DROP INDEX IF EXISTS idx_portfolios_due;

-- Remove mode, schedule, next_run_at columns.
ALTER TABLE portfolios
    DROP COLUMN mode,
    DROP COLUMN schedule,
    DROP COLUMN next_run_at;

-- Drop the now-unused enum type.
DROP TYPE IF EXISTS portfolio_mode;

-- Add date-period columns.
ALTER TABLE portfolios
    ADD COLUMN start_date DATE,
    ADD COLUMN end_date   DATE;

-- New scheduler index: open-ended portfolios not yet run today.
CREATE INDEX idx_portfolios_due ON portfolios (last_run_at NULLS FIRST)
    WHERE end_date IS NULL AND status IN ('ready', 'failed');
