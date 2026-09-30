-- Carteiras: a raiz do agregado financeiro.
--
-- O saldo é BIGINT em unidades mínimas (centavos). A moeda acompanha o valor
-- em coluna própria, de modo que valor e moeda são preservados exatamente,
-- sem ponto flutuante em nenhum ponto do caminho.

CREATE TABLE wallets (
    id             UUID        NOT NULL,
    player_id      UUID        NOT NULL,
    currency       CHAR(3)     NOT NULL,
    balance_minor  BIGINT      NOT NULL,
    version        BIGINT      NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_pk
        PRIMARY KEY (id),

    -- Não negatividade imposta pelo banco. Esta é a rede que vale mesmo se o
    -- lock da aplicação falhar, for removido por engano ou for contornado por
    -- um caminho de escrita novo.
    CONSTRAINT wallets_balance_non_negative
        CHECK (balance_minor >= 0),

    -- A versão nasce em 1 e só avança.
    CONSTRAINT wallets_version_positive
        CHECK (version >= 1),

    CONSTRAINT wallets_currency_iso4217
        CHECK (currency ~ '^[A-Z]{3}$')
);

-- (playerId, currency) identifica uma única carteira: é o que transforma a
-- segunda abertura em conflito em vez de em carteira duplicada.
CREATE UNIQUE INDEX wallets_player_currency_uk
    ON wallets (player_id, currency);
