-- Proteções de banco.
--
-- As invariantes acima já estão em constraints. O que falta são as garantias
-- que constraint não expressa: imutabilidade de linha, append-only e
-- privilégios. São elas que sustentam a auditabilidade mesmo diante de um
-- caminho de escrita novo, de um script manual ou de um bug na aplicação.

-- ---------------------------------------------------------------------------
-- Ledger: append-only.
-- ---------------------------------------------------------------------------

CREATE FUNCTION reject_ledger_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
        'wallet_ledger_entries é append-only: % recusado. Correções financeiras exigem novo lançamento.',
        TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_entries_no_update
    BEFORE UPDATE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TRIGGER wallet_ledger_entries_no_delete
    BEFORE DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

-- TRUNCATE não passa por trigger de linha: precisa do seu próprio gatilho,
-- senão é a porta aberta que anula as duas de cima.
CREATE TRIGGER wallet_ledger_entries_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION reject_ledger_mutation();

-- ---------------------------------------------------------------------------
-- Transações: estados terminais são imutáveis.
-- ---------------------------------------------------------------------------

CREATE FUNCTION reject_terminal_transition() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION
            'wager_transactions não admite DELETE: a trilha de auditoria é permanente.'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION
            'transação % está em estado terminal % e não admite novas transições (tentativa: %)',
            OLD.id, OLD.status, NEW.status
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- A identidade financeira não muda depois do aceite; só o desfecho muda.
    IF NEW.id           IS DISTINCT FROM OLD.id
    OR NEW.source       IS DISTINCT FROM OLD.source
    OR NEW.kind         IS DISTINCT FROM OLD.kind
    OR NEW.wallet_id    IS DISTINCT FROM OLD.wallet_id
    OR NEW.player_id    IS DISTINCT FROM OLD.player_id
    OR NEW.amount_minor IS DISTINCT FROM OLD.amount_minor
    OR NEW.currency     IS DISTINCT FROM OLD.currency
    OR NEW.provider_id  IS DISTINCT FROM OLD.provider_id
    OR NEW.external_transaction_id IS DISTINCT FROM OLD.external_transaction_id
    OR NEW.idempotency_key         IS DISTINCT FROM OLD.idempotency_key
    OR NEW.payload_hash            IS DISTINCT FROM OLD.payload_hash
    OR NEW.created_at              IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION
            'transação %: identidade e valor são imutáveis após o aceite', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wager_transactions_terminal_is_final
    BEFORE UPDATE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION reject_terminal_transition();

CREATE TRIGGER wager_transactions_no_delete
    BEFORE DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION reject_terminal_transition();

-- ---------------------------------------------------------------------------
-- Carteiras: disciplina de versão e imutabilidade de identidade.
-- ---------------------------------------------------------------------------

CREATE FUNCTION enforce_wallet_update_rules() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION
            'wallets não admite DELETE: o histórico financeiro depende da carteira.'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF NEW.id        IS DISTINCT FROM OLD.id
    OR NEW.player_id IS DISTINCT FROM OLD.player_id
    OR NEW.currency  IS DISTINCT FROM OLD.currency
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION
            'carteira %: identidade, jogador, moeda e criação são imutáveis', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- A versão avança exatamente um passo por mudança de saldo, e não avança
    -- sem mudança de saldo. É isso que torna a versão uma testemunha confiável
    -- de lost update em vez de um contador decorativo.
    IF NEW.balance_minor IS DISTINCT FROM OLD.balance_minor THEN
        IF NEW.version <> OLD.version + 1 THEN
            RAISE EXCEPTION
                'carteira %: mudança de saldo exige versão % + 1, recebida %',
                OLD.id, OLD.version, NEW.version
                USING ERRCODE = 'restrict_violation';
        END IF;
    ELSIF NEW.version IS DISTINCT FROM OLD.version THEN
        RAISE EXCEPTION
            'carteira %: versão avançou de % para % sem mudança de saldo',
            OLD.id, OLD.version, NEW.version
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallets_update_rules
    BEFORE UPDATE ON wallets
    FOR EACH ROW EXECUTE FUNCTION enforce_wallet_update_rules();

CREATE TRIGGER wallets_no_delete
    BEFORE DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION enforce_wallet_update_rules();

-- ---------------------------------------------------------------------------
-- Privilégios do papel da aplicação.
--
-- As triggers valem para qualquer papel, inclusive o dono do schema. Os grants
-- são a segunda camada: a aplicação sequer recebe o privilégio de tentar.
-- O papel é criado no bootstrap do PostgreSQL; o bloco é condicional para que
-- a migration continue aplicável em bancos de teste sem ele.
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'jungle_app') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON wallets, wager_transactions, inbox_messages, outbox_events TO jungle_app';

        -- Ledger: inserir e ler. Nunca alterar, nunca apagar.
        EXECUTE 'GRANT SELECT, INSERT ON wallet_ledger_entries TO jungle_app';
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON wallet_ledger_entries FROM jungle_app';

        EXECUTE 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO jungle_app';
        EXECUTE 'GRANT USAGE ON SCHEMA public TO jungle_app';
    END IF;
END;
$$;
