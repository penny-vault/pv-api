-- Enforce at most one queued/running run per portfolio. Closes a small race
-- window where two concurrent snapshot reads could each submit a fresh run
-- after both observed no in-flight one. The handler still does an opportunistic
-- ListRuns first; the index is the airtight backstop.
CREATE UNIQUE INDEX backtest_runs_one_inflight_per_portfolio
    ON backtest_runs (portfolio_id)
    WHERE status IN ('queued', 'running');
