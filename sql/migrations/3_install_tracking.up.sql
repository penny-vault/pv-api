-- 3_install_tracking.up.sql
-- Adds install-state tracking to the strategies registry. See
-- docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md "Strategy lifecycle".

ALTER TABLE strategies ADD COLUMN last_attempted_ver TEXT;
ALTER TABLE strategies ADD COLUMN install_error TEXT;
