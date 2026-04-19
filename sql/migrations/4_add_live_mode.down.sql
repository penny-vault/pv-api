-- 4_add_live_mode.down.sql
-- Postgres does not support dropping values from an enum. This migration is
-- therefore a no-op; a full rollback requires dropping and recreating the
-- portfolio_mode type and every column that references it, which is out of
-- scope for a reversible migration.

SELECT 1;
