BEGIN;

-- postgrest anonymous user
DO
$$
BEGIN
   IF EXISTS (
      SELECT FROM pg_catalog.pg_roles
      WHERE  rolname = 'pvanon') THEN
      RAISE NOTICE 'Role "pvanon" already exists. Skipping.';
   ELSE
      CREATE ROLE pvanon WITH NOINHERIT;
   END IF;
END
$$;

-- user group role
DO
$$
BEGIN
   IF EXISTS (
      SELECT FROM pg_catalog.pg_roles
      WHERE  rolname = 'pvuser') THEN
      RAISE NOTICE 'Role "pvuser" already exists. Skipping.';
   ELSE
      CREATE ROLE pvuser WITH NOLOGIN;
   END IF;
END
$$;

-- authenticator role
-- make sure you set the password
DO
$$
BEGIN
   IF EXISTS (
      SELECT FROM pg_catalog.pg_roles
      WHERE  rolname = 'pvapi') THEN

      RAISE NOTICE 'Role "pvapi" already exists. Skipping.';
   ELSE
      CREATE ROLE pvapi WITH NOINHERIT LOGIN CREATEROLE;
   END IF;
END
$$;

GRANT pvuser TO pvapi;
GRANT pvanon TO pvapi;

-- last changed function updates the lastchanged column to reflect the
-- current time
BEGIN;
CREATE OR REPLACE FUNCTION trigger_set_lastchanged()
RETURNS TRIGGER AS $$
BEGIN
  NEW.lastchanged = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- accounts
-- information about inidividual bank / brokerage accounts
-- can be a checking account, 401(k), house, etc.

CREATE TABLE IF NOT EXISTS accounts (
  id           BIGSERIAL PRIMARY KEY,
  user_id      TEXT DEFAULT current_user,
  name         TEXT NOT NULL CHECK (LENGTH(TRIM(BOTH user_id)) > 0),
  account_type TEXT NOT NULL,
  attributes   JSONB,
  institution  TEXT,

  value        NUMERIC DEFAULT 0.0,
  change       NUMERIC DEFAULT 0.0,
  change_pct   NUMERIC DEFAULT 0.0,

  rank         INTEGER DEFAULT 0,
  hidden       BOOLEAN DEFAULT false,
  open         BOOLEAN DEFAULT true,
  close_date   TIMESTAMP,
  separate     BOOLEAN DEFAULT false,

  created      TIMESTAMP NOT NULL DEFAULT now()
);

ALTER TABLE accounts ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON accounts
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON accounts TO pvuser;
GRANT USAGE, SELECT ON SEQUENCE accounts_id_seq TO pvuser;

-- transactions
-- transactions change the value of an account

CREATE TYPE tax_disposition AS ENUM ('LTC', 'STC', 'DEFERRED', 'ROTH', 'INCOME');

CREATE TABLE IF NOT EXISTS transactions (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id         TEXT DEFAULT current_user,
  account_id      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  source          TEXT DEFAULT NULL,
  source_id       TEXT DEFAULT NULL,
  sequence_num    BIGSERIAL NOT NULL,
  tx_date         DATE NOT NULL,
  payee           TEXT NOT NULL,
  category        JSONB DEFAULT '[{"category": "Uncategorized"}]'::jsonb,
  tags            TEXT[],
  reviewed        BOOLEAN DEFAULT 'f',
  cleared         BOOLEAN DEFAULT 'f',
  amount          MONEY,
  balance         MONEY,
  memo            TEXT,
  attachments     TEXT[], -- attachments are saved in object storage with the prefix <user_id>/<account_id>/<attachment-name>
  related         UUID[],

  -- stock transaction columns
  -- these are not used unless the transaction is recording a stock portfolio action
  commission      MONEY,
  composite_figi  CHARACTER(12),
  num_shares      NUMERIC(15, 5),
  price_per_share MONEY,
  ticker          TEXT,
  justification   JSONB,

  -- tax related
  tax_treatment   tax_disposition,
  gain_loss       NUMERIC(12, 5),

  -- audit columns
  created        TIMESTAMP DEFAULT now(),
  lastchanged    TIMESTAMP DEFAULT now(),
  UNIQUE (account_id, tx_date, sequence_num),
  UNIQUE (source, source_id)
);

ALTER TABLE transactions ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON transactions
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON transactions TO pvuser;
GRANT usage, select ON SEQUENCE transactions_sequence_num_seq TO pvuser;

CREATE TRIGGER set_lastchanged BEFORE UPDATE ON transactions
  FOR EACH ROW
    EXECUTE FUNCTION trigger_set_lastchanged();

-- networth
-- networth summary broken down by account
CREATE TABLE IF NOT EXISTS networth (
  user_id          TEXT DEFAULT current_user,
  measurement_date DATE NOT NULL,
  accounts         JSONB NOT NULL,
  PRIMARY KEY (user_id, measurement_date)
);

ALTER TABLE networth ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON networth
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON networth TO pvuser;

-- tags
CREATE TABLE IF NOT EXISTS tags (
  user_id TEXT DEFAULT current_user,
  name    TEXT NOT NULL,
  icon    TEXT,
  color   TEXT,
  PRIMARY KEY (user_id, name)
);

ALTER TABLE tags ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON tags
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON tags TO pvuser;

-- categories
CREATE TABLE IF NOT EXISTS categories (
  user_id   TEXT DEFAULT current_user,
  name      TEXT NOT NULL,
  icon      TEXT,
  blacklist BOOLEAN,
  PRIMARY KEY (user_id, name)
);

ALTER TABLE categories ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON categories
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON categories TO pvuser;

-- payees
CREATE TABLE IF NOT EXISTS payees (
  user_id          TEXT DEFAULT current_user,
  name             TEXT NOT NULL,
  icon             TEXT,
  renaming_rules   TEXT[],
  default_category TEXT,
  PRIMARY KEY (user_id, name)
);

ALTER TABLE payees ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON payees
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON payees TO pvuser;

-- holdings
CREATE TABLE IF NOT EXISTS holdings (
  user_id          TEXT DEFAULT current_user,
  event_date       DATE NOT NULL,
  composite_figi   CHARACTER(12),
  quantity         NUMERIC(15, 5),
  value            MONEY,
  PRIMARY KEY (user_id, event_date, composite_figi)
);

ALTER TABLE holdings ENABLE ROW LEVEL SECURITY;

CREATE POLICY user_id_policy ON holdings
    USING (user_id = current_user)
    WITH CHECK (user_id = current_user);

GRANT select, insert, update, delete ON holdings TO pvuser;

-- scf_networth_percentiles
CREATE TABLE IF NOT EXISTS scf_networth_percentiles (
  age_start  INTEGER,
  scf_year   INTEGER,
  amount     MONEY NOT NULL,
  percentile REAL NOT NULL,
  PRIMARY KEY (age_start, percentile, scf_year)
);

GRANT select, insert, update, delete ON scf_networth_percentiles TO pvuser;

-- arr_min
-- Find minimum value in an array
CREATE OR REPLACE FUNCTION arr_min(anyarray)
  RETURNS anyelement LANGUAGE sql IMMUTABLE PARALLEL SAFE AS
'SELECT min(i) FROM unnest($1) i';

-- clean_payees
-- Remove any payees that are not currently referenced by a transaction
CREATE OR REPLACE FUNCTION clean_payees() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  trx RECORD;
  tx_count INT;
BEGIN
FOR trx IN (SELECT name FROM payees) LOOP
    SELECT count(*) INTO tx_count FROM transactions WHERE payee=trx.name;
    IF tx_count = 0 THEN
      DELETE FROM payees WHERE name=trx.name;
    END IF;
  END LOOP;
END;
$$;

-- update_transaction_seq_nums
-- update the transaction sequence numbers for the given tx's
-- in_txs should be of the form [{"id": "trx-uuid", "sequence_num": 0, "tx_date": "2024-02-01"}]
-- transaction ids must all be from the same account
CREATE OR REPLACE FUNCTION update_transaction_seq_nums(
  in_txs JSONB,
  in_account_id BIGINT
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  obj             JSONB;
  trx             RECORD;
  tx_id           UUID;
  seq             BIGINT;
  dates           DATE[];
  min_date        DATE;
  running_balance MONEY;
  account_name TEXT;
	v_sqlstate TEXT;
	v_message TEXT;
	v_context TEXT;
BEGIN
  PERFORM pg_advisory_lock(in_account_id);

  -- first update sequence number to a negative number so we don't produce
  -- conflicts
  seq := 0;
  FOR obj IN SELECT * FROM jsonb_array_elements(in_txs) LOOP
    tx_id := (obj->>'id')::UUID;
    seq := seq - 1;
    UPDATE transactions SET sequence_num=seq WHERE id=tx_id;
  END LOOP;

  -- update sequence number for each transaction
  FOR obj IN SELECT * FROM jsonb_array_elements(in_txs) LOOP
    tx_id := (obj->>'id')::UUID;
    seq := (obj->>'sequence_num')::BIGINT;
    dates := array_append(dates, (obj->>'tx_date')::DATE);

    UPDATE transactions SET sequence_num=seq WHERE id=tx_id;
  END LOOP;

  min_date := arr_min(dates);

  -- re-calculate balances
  SELECT balance INTO running_balance FROM transactions t WHERE t.account_id = in_account_id AND t.tx_date < min_date ORDER BY t.tx_date DESC, t.sequence_num DESC LIMIT 1;
  IF running_balance IS NULL THEN
    running_balance := 0;
  END IF;

  FOR trx IN (SELECT t.id, t.tx_date, t.sequence_num, t.amount FROM transactions t WHERE t.account_id=in_account_id AND t.tx_date >= min_date ORDER BY t.tx_date, t.sequence_num) LOOP
    running_balance := running_balance + trx.amount;
    UPDATE transactions SET balance=running_balance WHERE id=trx.id;
  END LOOP;

  -- update account value
  UPDATE accounts SET value=running_balance WHERE accounts.id=in_account_id;

  -- update networth measurements
  SELECT name INTO account_name FROM accounts WHERE id=in_account_id;

  FOR trx IN (WITH tmp AS (
    SELECT t.balance as balance, extract(year FROM t.tx_date) AS yr, ROW_NUMBER() OVER (PARTITION BY t.account_id, extract(year FROM t.tx_date) ORDER BY t.tx_date DESC) AS sort_id FROM transactions t WHERE t.account_id=in_account_id AND t.balance IS NOT NULL
  ) SELECT yr, balance FROM tmp WHERE sort_id=1) LOOP
    INSERT INTO networth ("measurement_date", "accounts") VALUES ((trx.yr || '-12-31')::date, jsonb_build_object(account_name, trx.balance)) ON CONFLICT ON CONSTRAINT networth_pkey DO UPDATE SET accounts=(networth.accounts || jsonb_build_object(account_name, trx.balance));
  END LOOP;

  PERFORM pg_advisory_unlock(in_account_id);

  EXCEPTION
    WHEN foreign_key_violation THEN
      PERFORM pg_advisory_unlock(in_account_id);
      RAISE foreign_key_violation USING MESSAGE = 'account_id does not exist ' || in_account_id;

    WHEN OTHERS THEN
      PERFORM pg_advisory_unlock(in_account_id);
      GET STACKED DIAGNOSTICS
          v_sqlstate = returned_sqlstate,
          v_message = message_text,
          v_context = pg_exception_context;
        RAISE NOTICE 'sqlstate: %', v_sqlstate;
        RAISE NOTICE 'context: %', v_context;
        RAISE EXCEPTION '%', v_message;

END;
$$;

-- recalc_balance_history
-- update the balance of all transactions by date and sequence_num order
CREATE OR REPLACE FUNCTION recalc_balance_history(
  in_account_id BIGINT
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  tx_id UUID;
  running_balance MONEY;
  account_name TEXT;
  trx RECORD;
	v_sqlstate TEXT;
	v_message TEXT;
	v_context TEXT;
BEGIN
  PERFORM pg_advisory_lock(in_account_id);

  running_balance := 0;

  FOR trx IN (SELECT t.id, t.amount FROM transactions t WHERE t.account_id=in_account_id ORDER BY t.tx_date, t.sequence_num) LOOP
    running_balance := running_balance + trx.amount;
    UPDATE transactions SET balance=running_balance WHERE id=trx.id;
  END LOOP;

  -- update account value
  UPDATE accounts SET value=running_balance WHERE accounts.id=in_account_id;

  -- update networth measurements
  SELECT name INTO account_name FROM accounts WHERE id=in_account_id;

  -- clear all existing values
  FOR trx in SELECT measurement_date FROM networth LOOP
    UPDATE networth SET accounts=(networth.accounts - account_name) WHERE measurement_date=trx.measurement_date;
  END LOOP;

  DELETE FROM networth WHERE accounts = '{}'::jsonb;

  FOR trx IN (WITH tmp AS (
    SELECT t.balance as balance, extract(year FROM t.tx_date) AS yr, ROW_NUMBER() OVER (PARTITION BY t.account_id, extract(year FROM t.tx_date) ORDER BY t.tx_date DESC) AS sort_id FROM transactions t WHERE t.account_id=in_account_id AND t.balance IS NOT NULL
  ) SELECT yr, balance FROM tmp WHERE sort_id=1) LOOP
    INSERT INTO networth ("measurement_date", "accounts") VALUES ((trx.yr || '-12-31')::date, jsonb_build_object(account_name, trx.balance)) ON CONFLICT ON CONSTRAINT networth_pkey DO UPDATE SET accounts=(networth.accounts || jsonb_build_object(account_name, trx.balance));
  END LOOP;

  PERFORM pg_advisory_unlock(in_account_id);

  EXCEPTION
    WHEN foreign_key_violation THEN
      PERFORM pg_advisory_unlock(in_account_id);
      RAISE foreign_key_violation USING MESSAGE = 'account_id does not exist ' || in_account_id;

    WHEN OTHERS THEN
      PERFORM pg_advisory_unlock(in_account_id);
      GET STACKED DIAGNOSTICS
          v_sqlstate = returned_sqlstate,
          v_message = message_text,
          v_context = pg_exception_context;
        RAISE NOTICE 'sqlstate: %', v_sqlstate;
        RAISE NOTICE 'context: %', v_context;
        RAISE EXCEPTION '%', v_message;

END;
$$;

-- insert_transaction
-- create a new transaction and update all related tables
CREATE OR REPLACE FUNCTION insert_transaction(
  in_id              UUID, -- Use NULL to insert a new transaction
  in_account_id      BIGINT,
  in_source          TEXT,
  in_source_id       TEXT,
  in_sequence_num    BIGINT,
  in_tx_date         DATE,
  in_payee           TEXT,
  in_category        JSONB,
  in_tags            TEXT[],
  in_justification   JSONB,
  in_reviewed        BOOLEAN,
  in_cleared         BOOLEAN,
  in_amount          MONEY,
  in_memo            TEXT,
  in_related         UUID[],
  in_commission      NUMERIC(9, 2),
  in_composite_figi  TEXT,
  in_num_shares      NUMERIC(15, 5),
  in_price_per_share NUMERIC(15, 5),
  in_ticker          TEXT,
  in_tax_treatment   tax_disposition,
  in_gain_loss       NUMERIC(12, 5)
)
   RETURNS UUID
   LANGUAGE plpgsql
  AS
$$
DECLARE
  tx_id UUID;
  running_balance MONEY;
  account_name TEXT;
  orig_tx_date DATE;
  dirty_date DATE;
  orig_sequence_num BIGINT;
  is_moving_forward BOOLEAN;
  trx RECORD;
	v_sqlstate TEXT;
	v_message TEXT;
	v_context TEXT;
BEGIN
  IF in_id IS NULL THEN
    SELECT gen_random_uuid() INTO tx_id;
  ELSE
    tx_id := in_id;
  END IF;

  PERFORM pg_advisory_lock(in_account_id);

  SELECT tx_date, sequence_num INTO orig_tx_date, orig_sequence_num FROM transactions WHERE id=tx_id;

  IF not found THEN
    orig_tx_date := in_tx_date;
  END IF;

  IF in_sequence_num IS NULL THEN
    SELECT nextval('transactions_sequence_num_seq'::regclass) INTO in_sequence_num;
  END IF;

  -- Moving transaction forward in time; get balance of previous transaction
  is_moving_forward := false;
  IF (orig_tx_date < in_tx_date) OR (orig_tx_date = in_tx_date AND orig_sequence_num < in_sequence_num) THEN
    dirty_date := orig_tx_date;
    is_moving_forward := true;
    SELECT prev_balance INTO running_balance FROM (SELECT t.id, LAG(t.balance, 1) OVER (PARTITION BY t.account_id ORDER BY t.tx_date, t.sequence_num) AS prev_balance FROM transactions t WHERE t.account_id = in_account_id ORDER BY t.tx_date DESC, t.sequence_num DESC) tbl WHERE tbl.id=tx_id;
    IF running_balance IS NULL THEN
      running_balance := 0;
    END IF;
  END IF;

  INSERT INTO transactions (
    "id",
    "account_id",
    "source",
    "source_id",
    "sequence_num",
    "tx_date",
    "payee",
    "category",
    "tags",
    "justification",
    "reviewed",
    "cleared",
    "amount",
    "memo",
    "related",
    "commission",
    "composite_figi",
    "num_shares",
    "price_per_share",
    "ticker",
    "tax_treatment",
    "gain_loss"
  ) VALUES (
    tx_id,
    in_account_id,
    in_source,
    in_source_id,
    in_sequence_num,
    in_tx_date,
    in_payee,
    in_category,
    in_tags,
    in_justification,
    in_reviewed,
    in_cleared,
    in_amount,
    in_memo,
    in_related,
    in_commission,
    in_composite_figi,
    in_num_shares,
    in_price_per_share,
    in_ticker,
    in_tax_treatment,
    in_gain_loss
  ) ON CONFLICT ON CONSTRAINT transactions_pkey
  DO UPDATE SET
    source = EXCLUDED.source,
    source_id = EXCLUDED.source_id,
    sequence_num = EXCLUDED.sequence_num,
    tx_date = EXCLUDED.tx_date,
    payee = EXCLUDED.payee,
    category = EXCLUDED.category,
    tags = EXCLUDED.tags,
    justification = EXCLUDED.justification,
    reviewed = EXCLUDED.reviewed,
    cleared = EXCLUDED.cleared,
    amount = EXCLUDED.amount,
    memo = EXCLUDED.memo,
    related = EXCLUDED.related,
    commission = EXCLUDED.commission,
    composite_figi = EXCLUDED.composite_figi,
    num_shares = EXCLUDED.num_shares,
    price_per_share = EXCLUDED.price_per_share,
    ticker = EXCLUDED.ticker,
    tax_treatment = EXCLUDED.tax_treatment,
    gain_loss = EXCLUDED.gain_loss;

  -- get running balance and set dirty date when transaction moved earlier in time
  IF NOT is_moving_forward THEN
    dirty_date := in_tx_date;
    SELECT prev_balance INTO running_balance FROM (SELECT t.id, LAG(t.balance, 1) OVER (PARTITION BY t.account_id ORDER BY t.tx_date, t.sequence_num) AS prev_balance FROM transactions t WHERE t.tx_date <= dirty_date AND t.account_id = in_account_id ORDER BY t.tx_date DESC, t.sequence_num) tbl WHERE tbl.id=tx_id;
    IF running_balance IS NULL THEN
      running_balance := 0;
    END IF;
  END IF;

  FOR trx IN (SELECT t.id, t.tx_date, t.sequence_num, t.amount FROM transactions t WHERE t.account_id=in_account_id AND t.tx_date >= dirty_date IS NULL ORDER BY t.tx_date, t.sequence_num) LOOP
    IF is_moving_forward THEN
      IF trx.tx_date = dirty_date AND trx.sequence_num < orig_sequence_num THEN
        CONTINUE;
      END IF;
    ELSE
      IF trx.tx_date = dirty_date AND trx.sequence_num < in_sequence_num THEN
        CONTINUE;
      END IF;
    END IF;

    running_balance := running_balance + trx.amount;
    UPDATE transactions SET balance=running_balance WHERE id=trx.id;
  END LOOP;

  -- update account value
  UPDATE accounts SET value=running_balance WHERE accounts.id=in_account_id;

  -- update networth measurements
  SELECT name INTO account_name FROM accounts WHERE id=in_account_id;

  FOR trx IN (WITH tmp AS (
    SELECT t.balance as balance, extract(year FROM t.tx_date) AS yr, ROW_NUMBER() OVER (PARTITION BY t.account_id, extract(year FROM t.tx_date) ORDER BY t.tx_date DESC) AS sort_id FROM transactions t WHERE t.account_id=in_account_id AND t.balance IS NOT NULL
  ) SELECT yr, balance FROM tmp WHERE sort_id=1) LOOP
    INSERT INTO networth ("measurement_date", "accounts") VALUES ((trx.yr || '-12-31')::date, jsonb_build_object(account_name, trx.balance)) ON CONFLICT ON CONSTRAINT networth_pkey DO UPDATE SET accounts=(networth.accounts || jsonb_build_object(account_name, trx.balance));
  END LOOP;

  PERFORM pg_advisory_unlock(in_account_id);

  RETURN tx_id;

  EXCEPTION
    WHEN foreign_key_violation THEN
      PERFORM pg_advisory_unlock(in_account_id);
      RAISE foreign_key_violation USING MESSAGE = 'account_id does not exist ' || account_id;

    WHEN OTHERS THEN
      PERFORM pg_advisory_unlock(in_account_id);
      GET STACKED DIAGNOSTICS
          v_sqlstate = returned_sqlstate,
          v_message = message_text,
          v_context = pg_exception_context;
        RAISE NOTICE 'sqlstate: %', v_sqlstate;
        RAISE NOTICE 'context: %', v_context;
        RAISE EXCEPTION '%', v_message;
END;
$$;

-- delete_transaction
-- delete a transaction and update all related tables
CREATE OR REPLACE FUNCTION delete_transaction(
  in_id              UUID
)
   RETURNS void
   LANGUAGE plpgsql
  AS
$$
DECLARE
  trx_account_id BIGINT;
  trx_date DATE;
  trx_sequence_num INTEGER;
  trx_amount MONEY;
  trx_balance MONEY;
  running_balance MONEY;
  account_name TEXT;
  trx RECORD;
	v_sqlstate TEXT;
	v_message TEXT;
	v_context TEXT;
BEGIN
  IF in_id IS NULL THEN
    RAISE null_value_not_allowed USING MESSAGE = 'id must not be null';
  END IF;

  -- Get account id and previous balance
  SELECT t.account_id, t.amount, t.balance, t.tx_date, t.sequence_num INTO trx_account_id, trx_amount, trx_balance, trx_date, trx_sequence_num FROM transactions t WHERE id=in_id;
  running_balance := trx_balance - trx_amount;

  PERFORM pg_advisory_lock(trx_account_id);

  -- remove transaction
  DELETE FROM transactions WHERE id=in_id;

  -- update balances
  FOR trx IN (SELECT t.id, t.amount FROM transactions t WHERE t.account_id=trx_account_id AND t.tx_date >= trx_date ORDER BY t.tx_date, t.sequence_num) LOOP
    running_balance := running_balance + trx.amount;
    UPDATE transactions SET balance=running_balance WHERE id=trx.id;
  END LOOP;

  -- update account value
  UPDATE accounts SET value=running_balance WHERE accounts.id=trx_account_id;

  -- update networth measurements
  SELECT name INTO account_name FROM accounts WHERE id=trx_account_id;

  FOR trx IN (WITH tmp AS (
    SELECT t.balance as balance, extract(year FROM t.tx_date) AS yr, ROW_NUMBER() OVER (PARTITION BY t.account_id, extract(year FROM t.tx_date) ORDER BY t.tx_date DESC) AS sort_id FROM transactions t WHERE t.account_id=trx_account_id AND t.balance IS NOT NULL
  ) SELECT yr, balance FROM tmp WHERE sort_id=1) LOOP
    INSERT INTO networth ("measurement_date", "accounts") VALUES ((trx.yr || '-12-31')::date, jsonb_build_object(account_name, trx.balance)) ON CONFLICT ON CONSTRAINT networth_pkey DO UPDATE SET accounts=(networth.accounts || jsonb_build_object(account_name, trx.balance));
  END LOOP;

  PERFORM pg_advisory_unlock(trx_account_id);

  EXCEPTION
    WHEN foreign_key_violation THEN
      PERFORM pg_advisory_unlock(trx_account_id);
      RAISE foreign_key_violation USING MESSAGE = 'account_id does not exist ' || account_id;

    WHEN OTHERS THEN
      PERFORM pg_advisory_unlock(trx_account_id);
      GET STACKED DIAGNOSTICS
          v_sqlstate = returned_sqlstate,
          v_message = message_text,
          v_context = pg_exception_context;
        RAISE NOTICE 'sqlstate: %', v_sqlstate;
        RAISE NOTICE 'context: %', v_context;
        RAISE EXCEPTION '%', v_message;
END;
$$;

COMMIT;
