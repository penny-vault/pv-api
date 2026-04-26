ALTER TABLE portfolios
    DROP CONSTRAINT IF EXISTS portfolios_run_retention_min;

ALTER TABLE portfolios
    DROP COLUMN IF EXISTS run_retention;
