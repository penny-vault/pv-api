-- pvapi 3.0 initial schema.

-- Enum types used across the schema.
CREATE TYPE artifact_kind AS ENUM ('binary', 'image');
CREATE TYPE portfolio_mode AS ENUM ('one_shot', 'continuous');
CREATE TYPE portfolio_status AS ENUM ('pending', 'running', 'ready', 'failed');
CREATE TYPE run_status AS ENUM ('queued', 'running', 'success', 'failed');

-- Registry of every strategy pvapi knows about.
-- Official strategies are discovered from github.com/penny-vault; unofficial
-- strategies are user-registered and scoped to a single owner.
CREATE TABLE strategies (
    short_code      TEXT PRIMARY KEY,
    repo_owner      TEXT NOT NULL,
    repo_name       TEXT NOT NULL,
    clone_url       TEXT NOT NULL,
    is_official     BOOLEAN NOT NULL DEFAULT FALSE,
    owner_sub       TEXT,
    description     TEXT,
    categories      TEXT[],
    stars           INTEGER,
    installed_ver   TEXT,
    installed_at    TIMESTAMPTZ,
    artifact_kind   artifact_kind,
    artifact_ref    TEXT,
    describe_json   JSONB,
    cagr            DOUBLE PRECISION,
    max_drawdown    DOUBLE PRECISION,
    sharpe          DOUBLE PRECISION,
    stats_as_of     TIMESTAMPTZ,
    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((is_official AND owner_sub IS NULL) OR (NOT is_official AND owner_sub IS NOT NULL))
);
CREATE INDEX idx_strategies_official ON strategies (is_official);
CREATE INDEX idx_strategies_owner ON strategies (owner_sub) WHERE owner_sub IS NOT NULL;

-- Portfolios are the unit of user-facing configuration + cached results.
-- The runner writes derived summary columns and JSONB blobs on each successful
-- backtest; scalar columns exist so the list endpoint can sort/filter without
-- parsing JSON.
CREATE TABLE portfolios (
    id                    UUID PRIMARY KEY DEFAULT uuidv7(),
    owner_sub             TEXT NOT NULL,
    slug                  TEXT NOT NULL,
    name                  TEXT NOT NULL,
    strategy_code         TEXT NOT NULL REFERENCES strategies(short_code),
    strategy_ver          TEXT NOT NULL,
    parameters            JSONB NOT NULL,
    preset_name           TEXT,
    benchmark             TEXT NOT NULL DEFAULT 'SPY',
    mode                  portfolio_mode NOT NULL,
    schedule              TEXT,
    status                portfolio_status NOT NULL DEFAULT 'pending',
    inception_date        DATE,
    snapshot_path         TEXT,
    last_run_at           TIMESTAMPTZ,
    next_run_at           TIMESTAMPTZ,
    last_error            TEXT,
    current_value         DOUBLE PRECISION,
    ytd_return            DOUBLE PRECISION,
    max_drawdown          DOUBLE PRECISION,
    sharpe                DOUBLE PRECISION,
    cagr_since_inception  DOUBLE PRECISION,
    summary_json          JSONB,
    drawdowns_json        JSONB,
    metrics_json          JSONB,
    trailing_json         JSONB,
    allocation_json       JSONB,
    current_assets        TEXT[],
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_sub, slug)
);
CREATE INDEX idx_portfolios_owner ON portfolios (owner_sub);
CREATE INDEX idx_portfolios_due ON portfolios (next_run_at)
    WHERE mode = 'continuous' AND status IN ('ready', 'failed');

-- One row per Run invocation. Kept for history; live status lives on portfolios.
CREATE TABLE backtest_runs (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    portfolio_id    UUID NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
    status          run_status NOT NULL,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    duration_ms     INTEGER,
    error           TEXT,
    snapshot_path   TEXT
);
CREATE INDEX idx_runs_portfolio ON backtest_runs (portfolio_id, started_at DESC);
