BEGIN;
ALTER TABLE portfolios
    ADD COLUMN summary_json     JSONB,
    ADD COLUMN drawdowns_json   JSONB,
    ADD COLUMN metrics_json     JSONB,
    ADD COLUMN trailing_json    JSONB,
    ADD COLUMN allocation_json  JSONB,
    ADD COLUMN current_assets   TEXT[];
COMMIT;
