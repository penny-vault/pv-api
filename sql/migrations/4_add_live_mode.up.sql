-- 4_add_live_mode.up.sql
-- Reserves `live` in the portfolio_mode enum. The POST /portfolios handler
-- rejects mode=live with 422 for the entirety of the 3.0 rewrite; real live
-- trading is a separate future project (see design spec § Live trading).
--
-- Postgres lets us ALTER TYPE ... ADD VALUE outside a transaction so long as
-- the value has not been used yet. golang-migrate does not wrap PG migrations
-- in an implicit transaction; this single-statement file runs as-is.

ALTER TYPE portfolio_mode ADD VALUE IF NOT EXISTS 'live';
