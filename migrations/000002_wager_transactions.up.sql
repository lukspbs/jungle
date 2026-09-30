-- Transações de aposta: operações externas dos provedores e a abertura
-- interna da carteira, numa tabela só.
--
-- A tabela é única porque o ledger referencia uma transação, e a abertura
-- também produz lançamento. A distinção entre origem interna e externa fica a
-- cargo da coluna source e das constraints condicionais abaixo, que é o que o
-- desafio exige do schema.

CREATE TYPE wager_source AS ENUM ('INTERNAL', 'EXTERNAL');

CREATE TYPE wager_kind AS ENUM ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK');

CREATE TYPE wager_status AS ENUM (
    'PENDING',            -- aceito, processamento não concluído
    'PENDING_REFERENCE',  -- aguardando referência ainda indisponível
    'PROCESSED',          -- terminal, com sucesso
    'REJECTED',           -- terminal, recusado por regra de negócio
    'FAILED'              -- terminal, falha permanente de infraestrutura
);

CREATE TABLE wager_transactions (
    id                     UUID         NOT NULL,
    source                 wager_source NOT NULL,
    kind                   wager_kind   NOT NULL,
    status                 wager_status NOT NULL,

    wallet_id              UUID         NOT NULL,
    player_id              UUID         NOT NULL,

    amount_minor           BIGINT       NOT NULL,
    currency               CHAR(3)      NOT NULL,

    -- Metadados externos. Inaplicáveis à abertura interna, e por isso NULL lá.
    provider_id            TEXT,
    external_transaction_id TEXT,
    idempotency_key        TEXT,
    payload_hash           BYTEA,
    round_id               TEXT,
    game_id                TEXT,

    -- Referência de reversão: a informada pelo provedor e a resolvida por nós.
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID,

    -- Retomada durável do worker de referências pendentes.
    reference_attempts        INTEGER     NOT NULL DEFAULT 0,
    reference_next_attempt_at TIMESTAMPTZ,
    reference_expires_at      TIMESTAMPTZ,

    -- Resultado devolvido ao provedor. O saldo é o observado no processamento
    -- original: um replay precisa reproduzi-lo mesmo que a carteira já tenha
    -- se movimentado depois, então ele é congelado aqui e nunca relido.
    result_balance_minor   BIGINT,
    failure_code           TEXT,

    created_at             TIMESTAMPTZ  NOT NULL,
    updated_at             TIMESTAMPTZ  NOT NULL,

    CONSTRAINT wager_transactions_pk
        PRIMARY KEY (id),

    CONSTRAINT wager_transactions_wallet_fk
        FOREIGN KEY (wallet_id) REFERENCES wallets (id),

    CONSTRAINT wager_transactions_reference_fk
        FOREIGN KEY (reference_transaction_id) REFERENCES wager_transactions (id),

    CONSTRAINT wager_transactions_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$'),

    -- Origem e tipo têm que concordar: OPENING é exclusivamente interno, e os
    -- cinco tipos externos são exclusivamente externos. É esta constraint que
    -- torna impossível um provedor gravar OPENING mesmo que a validação da
    -- aplicação seja contornada.
    CONSTRAINT wager_transactions_source_matches_kind
        CHECK (
            (source = 'INTERNAL' AND kind = 'OPENING')
            OR
            (source = 'EXTERNAL' AND kind <> 'OPENING')
        ),

    -- A abertura interna não carrega metadados externos.
    CONSTRAINT wager_transactions_internal_has_no_external_metadata
        CHECK (
            source <> 'INTERNAL'
            OR (
                provider_id IS NULL
                AND external_transaction_id IS NULL
                AND idempotency_key IS NULL
                AND payload_hash IS NULL
                AND round_id IS NULL
                AND game_id IS NULL
                AND reference_external_transaction_id IS NULL
                AND reference_transaction_id IS NULL
            )
        ),

    -- A operação externa carrega todos eles.
    CONSTRAINT wager_transactions_external_requires_metadata
        CHECK (
            source <> 'EXTERNAL'
            OR (
                provider_id IS NOT NULL
                AND external_transaction_id IS NOT NULL
                AND idempotency_key IS NOT NULL
                AND payload_hash IS NOT NULL
                AND round_id IS NOT NULL
                AND game_id IS NOT NULL
            )
        ),

    -- Reversões exigem referência externa; os demais tipos não a admitem.
    CONSTRAINT wager_transactions_reversal_requires_reference
        CHECK (
            (kind IN ('REFUND', 'ROLLBACK'))
            = (reference_external_transaction_id IS NOT NULL)
        ),

    -- Política de valor por tipo. LOSS é exatamente zero; os demais tipos com
    -- movimentação exigem valor positivo.
    CONSTRAINT wager_transactions_amount_policy
        CHECK (
            (kind = 'LOSS' AND amount_minor = 0)
            OR
            (kind <> 'LOSS' AND amount_minor > 0)
        ),

    -- failureCode pertence aos estados de recusa e falha, e só a eles.
    CONSTRAINT wager_transactions_failure_code_scope
        CHECK (
            (status IN ('REJECTED', 'FAILED')) OR failure_code IS NULL
        ),

    -- O saldo resultante existe exatamente quando a operação foi concluída.
    CONSTRAINT wager_transactions_result_balance_scope
        CHECK (
            (status = 'PROCESSED') = (result_balance_minor IS NOT NULL)
        ),

    CONSTRAINT wager_transactions_result_balance_non_negative
        CHECK (result_balance_minor IS NULL OR result_balance_minor >= 0),

    CONSTRAINT wager_transactions_reference_attempts_non_negative
        CHECK (reference_attempts >= 0)
);

-- Idempotência persistente, imposta pelo banco.
--
-- Dois índices distintos porque o desafio pede duas garantias diferentes:
-- a chave de idempotência não pode ser reutilizada, e a operação financeira
-- identificada por (provider, externalTransactionId) não pode ser reaplicada
-- sob outra chave. Um índice só não cobre os dois casos.
CREATE UNIQUE INDEX wager_transactions_provider_external_uk
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE source = 'EXTERNAL';

CREATE UNIQUE INDEX wager_transactions_provider_idempotency_uk
    ON wager_transactions (provider_id, idempotency_key)
    WHERE source = 'EXTERNAL';

-- Crédito inicial duplicado é impossível: uma carteira tem no máximo uma
-- abertura.
CREATE UNIQUE INDEX wager_transactions_single_opening_uk
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- Uma referência não recebe duas reversões bem-sucedidas do mesmo tipo.
-- Parcial em PROCESSED: tentativas rejeitadas não consomem a cota, e um
-- REFUND e um ROLLBACK sobre a mesma aposta continuam sendo linhas distintas,
-- resolvidos pela regra de negócio documentada em ARCHITECTURE.md.
CREATE UNIQUE INDEX wager_transactions_single_reversal_uk
    ON wager_transactions (reference_transaction_id, kind)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

-- Resolução de referência por (provider, externalTransactionId).
CREATE INDEX wager_transactions_wallet_idx
    ON wager_transactions (wallet_id);

-- Fila do worker de referências pendentes.
CREATE INDEX wager_transactions_pending_reference_idx
    ON wager_transactions (reference_next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

-- Retomada de PENDING órfão após queda de instância.
CREATE INDEX wager_transactions_pending_idx
    ON wager_transactions (created_at)
    WHERE status = 'PENDING';
