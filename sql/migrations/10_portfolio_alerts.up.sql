CREATE TABLE portfolio_alerts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    portfolio_id     UUID NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
    frequency        TEXT NOT NULL CHECK (frequency IN ('scheduled_run','daily','weekly','monthly')),
    recipients       TEXT[] NOT NULL,
    last_sent_at     TIMESTAMPTZ,
    last_sent_value  NUMERIC,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_alerts_portfolio ON portfolio_alerts(portfolio_id);
