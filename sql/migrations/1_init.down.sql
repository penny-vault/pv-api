-- Reverses the 1_init.up.sql schema. Drops in reverse dependency order.

DROP TABLE IF EXISTS backtest_runs;
DROP TABLE IF EXISTS portfolios;
DROP TABLE IF EXISTS strategies;

DROP TYPE IF EXISTS run_status;
DROP TYPE IF EXISTS portfolio_status;
DROP TYPE IF EXISTS portfolio_mode;
DROP TYPE IF EXISTS artifact_kind;
