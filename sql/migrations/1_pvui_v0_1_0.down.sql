BEGIN;

DROP FUNCTION IF EXISTS trigger_set_lastchanged;

DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS networth;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS categories;
DROP TABLE IF EXISTS payees;
DROP TABLE IF EXISTS scf_networth_percentiles;

COMMIT;

DROP TYPE IF EXISTS tax_disposition;

DROP FUNCTION IF EXISTS arr_min;
DROP FUNCTION IF EXISTS clean_payees;
DROP FUNCTION IF EXISTS update_transaction_seq_nums;
DROP FUNCTION IF EXISTS recalc_balance_history;
DROP FUNCTION IF EXISTS insert_transaction;
DROP FUNCTION IF EXISTS delete_transaction;
