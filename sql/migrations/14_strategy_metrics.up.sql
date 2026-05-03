ALTER TABLE strategies
    ADD COLUMN ulcer_index           DOUBLE PRECISION,
    ADD COLUMN beta                  DOUBLE PRECISION,
    ADD COLUMN alpha                 DOUBLE PRECISION,
    ADD COLUMN std_dev               DOUBLE PRECISION,
    ADD COLUMN tax_cost_ratio        DOUBLE PRECISION,
    ADD COLUMN one_year_return       DOUBLE PRECISION,
    ADD COLUMN ytd_return            DOUBLE PRECISION,
    ADD COLUMN benchmark_ytd_return  DOUBLE PRECISION;
