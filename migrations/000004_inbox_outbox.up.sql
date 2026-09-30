-- Inbox e outbox.
--
-- Ambas participam da mesma transação SQL das alterações de domínio: é o que
-- torna atômicos o efeito financeiro, o registro de consumo e a intenção de
-- publicar. Nenhum evento é publicado antes do commit que o originou.

CREATE TABLE inbox_messages (
    consumer_name TEXT        NOT NULL,
    message_id    TEXT        NOT NULL,
    payload_hash  BYTEA       NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ,

    -- Unicidade de (consumerName, messageId): a reentrega at-least-once do SQS
    -- colide aqui antes de chegar ao domínio.
    CONSTRAINT inbox_messages_pk
        PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
    -- Identidade estável do evento. Republicações preservam este id, de modo
    -- que o consumidor a jusante consegue deduplicar.
    event_id        UUID        NOT NULL,

    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INTEGER     NOT NULL,

    -- Snapshot imutável do payload no instante do commit.
    payload         JSONB       NOT NULL,

    correlation_id  TEXT        NOT NULL,
    causation_id    TEXT,

    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    published_at    TIMESTAMPTZ,

    -- Lease do publisher. Permite que múltiplos publishers disputem registros
    -- sem segurar transação aberta durante a chamada de rede, e que trabalho
    -- abandonado por uma instância morta seja assumido por outra quando o
    -- lease expira.
    locked_by       TEXT,
    locked_until    TIMESTAMPTZ,

    CONSTRAINT outbox_events_pk
        PRIMARY KEY (event_id),

    CONSTRAINT outbox_events_attempts_non_negative
        CHECK (attempts >= 0),

    CONSTRAINT outbox_events_version_positive
        CHECK (event_version >= 1),

    CONSTRAINT outbox_events_lease_paired
        CHECK ((locked_by IS NULL) = (locked_until IS NULL))
);

-- Fila de publicação: só as pendentes entram no índice, então ele não cresce
-- com o histórico já publicado.
CREATE INDEX outbox_events_pending_idx
    ON outbox_events (next_attempt_at)
    WHERE published_at IS NULL;

CREATE INDEX outbox_events_aggregate_idx
    ON outbox_events (aggregate_type, aggregate_id);
