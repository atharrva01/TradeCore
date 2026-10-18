-- All money and prices are BIGINT paise; quantities are whole shares.

CREATE TABLE instruments (
    symbol        TEXT PRIMARY KEY,
    name          TEXT    NOT NULL,
    tick_size     BIGINT  NOT NULL CHECK (tick_size > 0),
    initial_price BIGINT  NOT NULL CHECK (initial_price > 0),
    last_price    BIGINT  NOT NULL CHECK (last_price > 0),
    high          BIGINT  NOT NULL,
    low           BIGINT  NOT NULL,
    volume        BIGINT  NOT NULL CHECK (volume >= 0),
    halted        BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT UNIQUE,
    password_hash TEXT,
    display_name  TEXT        NOT NULL,
    is_bot        BOOLEAN     NOT NULL DEFAULT false,
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- asset is 'INR' for cash or an instrument symbol for shares.
CREATE TABLE balances (
    user_id   TEXT   NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    asset     TEXT   NOT NULL,
    available BIGINT NOT NULL CHECK (available >= 0),
    reserved  BIGINT NOT NULL CHECK (reserved >= 0),
    PRIMARY KEY (user_id, asset)
);

CREATE TABLE positions (
    user_id  TEXT   NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    symbol   TEXT   NOT NULL REFERENCES instruments (symbol),
    quantity BIGINT NOT NULL CHECK (quantity >= 0),
    cost     BIGINT NOT NULL,
    realized BIGINT NOT NULL,
    PRIMARY KEY (user_id, symbol)
);

CREATE TABLE orders (
    id         TEXT PRIMARY KEY,
    seq        BIGINT NOT NULL UNIQUE,
    user_id    TEXT   NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    symbol     TEXT   NOT NULL REFERENCES instruments (symbol),
    side       TEXT   NOT NULL CHECK (side IN ('BUY', 'SELL')),
    type       TEXT   NOT NULL CHECK (type IN ('LIMIT', 'MARKET')),
    price      BIGINT NOT NULL CHECK (price >= 0),
    quantity   BIGINT NOT NULL CHECK (quantity > 0),
    filled_qty BIGINT NOT NULL CHECK (filled_qty >= 0 AND filled_qty <= quantity),
    status     TEXT   NOT NULL CHECK (status IN ('OPEN', 'PARTIALLY_FILLED', 'FILLED', 'CANCELLED')),
    created_at BIGINT NOT NULL
);

-- Crash recovery reads only resting orders, in arrival order.
CREATE INDEX orders_resting ON orders (symbol, seq) WHERE status IN ('OPEN', 'PARTIALLY_FILLED');
CREATE INDEX orders_by_user ON orders (user_id, created_at DESC);

-- Trades reference orders and users by ID without foreign keys so that
-- history survives when bot orders and expired guests are pruned.
CREATE TABLE trades (
    seq            BIGSERIAL PRIMARY KEY,
    id             TEXT    NOT NULL UNIQUE,
    symbol         TEXT    NOT NULL,
    buy_order_id   TEXT    NOT NULL,
    sell_order_id  TEXT    NOT NULL,
    buyer_id       TEXT    NOT NULL,
    seller_id      TEXT    NOT NULL,
    price          BIGINT  NOT NULL CHECK (price > 0),
    quantity       BIGINT  NOT NULL CHECK (quantity > 0),
    taker_side     TEXT    NOT NULL,
    created_at     BIGINT  NOT NULL,
    involves_human BOOLEAN NOT NULL
);

CREATE INDEX trades_by_symbol ON trades (symbol, seq DESC);
CREATE INDEX trades_bot_only_age ON trades (created_at) WHERE NOT involves_human;

CREATE TABLE refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);

-- Single row holding the ID counters so IDs never repeat after a restart,
-- and the writer epoch: each process bumps it at boot and every commit checks
-- it, so a stale process left running during a deploy cannot overwrite state.
CREATE TABLE exchange_state (
    id        INT PRIMARY KEY CHECK (id = 1),
    order_seq BIGINT NOT NULL,
    trade_seq BIGINT NOT NULL,
    epoch     BIGINT NOT NULL
);
