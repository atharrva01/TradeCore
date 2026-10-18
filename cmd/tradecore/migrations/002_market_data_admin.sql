-- Trading-day fields and per-instrument event sequences.
ALTER TABLE instruments
    ADD COLUMN day_open  BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN day_start BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN seq       BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN trade_seq BIGINT NOT NULL DEFAULT 0;
UPDATE instruments SET day_open = last_price WHERE day_open = 0;

ALTER TABLE users ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE orders
    ADD COLUMN tif TEXT NOT NULL DEFAULT 'GTC' CHECK (tif IN ('GTC', 'IOC')),
    DROP CONSTRAINT orders_status_check,
    ADD CONSTRAINT orders_status_check
        CHECK (status IN ('OPEN', 'PARTIALLY_FILLED', 'FILLED', 'CANCELLED', 'REJECTED'));

-- The instrument's trade sequence, so clients can detect gaps after a restart.
ALTER TABLE trades ADD COLUMN symbol_seq BIGINT NOT NULL DEFAULT 0;
CREATE INDEX trades_by_user_buyer ON trades (buyer_id, seq DESC) WHERE involves_human;
CREATE INDEX trades_by_user_seller ON trades (seller_id, seq DESC) WHERE involves_human;

CREATE TABLE candles (
    symbol       TEXT   NOT NULL,
    interval     TEXT   NOT NULL CHECK (interval IN ('1m', '5m', '1h', '1d')),
    bucket_start BIGINT NOT NULL,
    open         BIGINT NOT NULL,
    high         BIGINT NOT NULL,
    low          BIGINT NOT NULL,
    close        BIGINT NOT NULL,
    volume       BIGINT NOT NULL,
    PRIMARY KEY (symbol, interval, bucket_start)
);

ALTER TABLE exchange_state ADD COLUMN bot_settings JSONB;
