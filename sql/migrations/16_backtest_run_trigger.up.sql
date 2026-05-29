-- Record whether a backtest run was started by the nightly scheduler or by a
-- manual user action. ClaimDue gates the daily run on scheduled runs only, so a
-- manual "Run now" no longer counts as the day's scheduled run -- which would
-- otherwise suppress both the scheduled run and its alert email. Manual runs
-- also skip the alert path entirely.
--
-- Existing rows are backfilled to 'scheduled' to preserve the prior
-- "already ran today" semantics across the migration; the column default then
-- flips to 'manual' so any future insert that omits the value errs toward the
-- non-suppressing, non-emailing case.
ALTER TABLE backtest_runs
    ADD COLUMN triggered_by TEXT NOT NULL DEFAULT 'scheduled'
        CHECK (triggered_by IN ('scheduled', 'manual'));

ALTER TABLE backtest_runs ALTER COLUMN triggered_by SET DEFAULT 'manual';
