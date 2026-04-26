ALTER TABLE portfolios
    ADD COLUMN run_retention INT NOT NULL DEFAULT 2;

ALTER TABLE portfolios
    ADD CONSTRAINT portfolios_run_retention_min CHECK (run_retention >= 1);
