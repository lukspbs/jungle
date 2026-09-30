-- Ledger append-only da carteira.
--
-- Cada lançamento carrega o saldo antes e depois, de modo que a linha é
-- auditável isoladamente e a reconciliação pode reconstruir o saldo sem
-- depender da ordem de leitura.

CREATE TYPE ledger_direction AS ENUM ('DEBIT', 'CREDIT');

CREATE TABLE wallet_ledger_entries (
    id                   UUID             NOT NULL,

    -- Sequência monotônica interna. É o que dá ao cursor de paginação uma
    -- ordenação estável: created_at empata entre lançamentos do mesmo commit.
    seq                  BIGINT           GENERATED ALWAYS AS IDENTITY,

    wallet_id            UUID             NOT NULL,
    transaction_id       UUID             NOT NULL,
    direction            ledger_direction NOT NULL,

    amount_minor         BIGINT           NOT NULL,
    currency             CHAR(3)          NOT NULL,
    balance_before_minor BIGINT           NOT NULL,
    balance_after_minor  BIGINT           NOT NULL,

    created_at           TIMESTAMPTZ      NOT NULL,

    CONSTRAINT wallet_ledger_entries_pk
        PRIMARY KEY (id),

    CONSTRAINT wallet_ledger_entries_wallet_fk
        FOREIGN KEY (wallet_id) REFERENCES wallets (id),

    CONSTRAINT wallet_ledger_entries_transaction_fk
        FOREIGN KEY (transaction_id) REFERENCES wager_transactions (id),

    -- Um lançamento sem valor não é um lançamento: LOSS e operações rejeitadas
    -- não produzem linha aqui.
    CONSTRAINT wallet_ledger_entries_amount_positive
        CHECK (amount_minor > 0),

    CONSTRAINT wallet_ledger_entries_balances_non_negative
        CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),

    CONSTRAINT wallet_ledger_entries_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$'),

    -- balanceAfter = balanceBefore ± money, verificado pelo banco e não só
    -- pelo construtor do domínio.
    CONSTRAINT wallet_ledger_entries_balance_arithmetic
        CHECK (
            (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor)
            OR
            (direction = 'DEBIT'  AND balance_after_minor = balance_before_minor - amount_minor)
        )
);

-- Uma transação move uma carteira no máximo uma vez. É esta constraint que
-- transforma um reprocessamento acidental em erro de unicidade em vez de em
-- movimentação duplicada.
CREATE UNIQUE INDEX wallet_ledger_entries_wallet_transaction_uk
    ON wallet_ledger_entries (wallet_id, transaction_id);

-- Paginação por cursor opaco e reconstrução do saldo na reconciliação.
CREATE UNIQUE INDEX wallet_ledger_entries_wallet_seq_idx
    ON wallet_ledger_entries (wallet_id, seq);
