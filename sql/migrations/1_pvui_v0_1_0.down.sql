BEGIN;

-- postgrest anonymous user
DROP ROLE pvanon;

-- user group role
DROP ROLE pvuser;

-- authenticator role
DROP ROLE pvapi;

DROP FUNCTION trigger_set_lastchanged;

DROP TABLE accounts;
DROP TYPE tax_disposition;
DROP TABLE transactions;
DROP TABLE networth;
DROP TABLE tags;
DROP TABLE categories;
DROP TABLE payees;
DROP TABLE scf_networth_percentiles;

DROP FUNCTION arr_min;
DROP FUNCTION clean_payees;
DROP FUNCTION update_transaction_seq_nums;
DROP FUNCTION recalc_balance_history;
DROP FUNCTION insert_transaction;
DROP FUNCTION delete_transaction;

COMMIT;
