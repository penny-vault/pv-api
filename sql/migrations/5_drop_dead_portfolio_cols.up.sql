BEGIN;
ALTER TABLE portfolios
    DROP COLUMN summary_json,
    DROP COLUMN drawdowns_json,
    DROP COLUMN metrics_json,
    DROP COLUMN trailing_json,
    DROP COLUMN allocation_json,
    DROP COLUMN current_assets;
COMMIT;
