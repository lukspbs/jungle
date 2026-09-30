-- Verificação das proteções de banco.
--
-- Cada caso executa um comando que o schema precisa recusar. Se o banco
-- aceitar, o script falha. É a contraprova das constraints e triggers: sem
-- ela, uma constraint escrita errado passa despercebida.

CREATE OR REPLACE FUNCTION assert_rejected(descricao TEXT, comando TEXT) RETURNS VOID AS $$
BEGIN
    BEGIN
        EXECUTE comando;
    EXCEPTION
        WHEN check_violation
          OR unique_violation
          OR restrict_violation
          OR foreign_key_violation
          OR not_null_violation THEN
            RAISE NOTICE 'OK      | %', descricao;
            RETURN;
    END;
    RAISE EXCEPTION 'FALHA   | % <- o banco aceitou o que deveria recusar', descricao;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION assert_accepted(descricao TEXT, comando TEXT) RETURNS VOID AS $$
BEGIN
    EXECUTE comando;
    RAISE NOTICE 'OK      | %', descricao;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- Cenário base: carteira aberta com 1000.00 BRL.
-- ---------------------------------------------------------------------------

INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
VALUES ('0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
        'BRL', 100000, 1, now(), now());

INSERT INTO wager_transactions
    (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
     result_balance_minor, created_at, updated_at)
VALUES
    ('0192f298-0000-7000-8000-000000000010', 'INTERNAL', 'OPENING', 'PROCESSED',
     '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
     100000, 'BRL', 100000, now(), now());

INSERT INTO wallet_ledger_entries
    (id, wallet_id, transaction_id, direction, amount_minor, currency,
     balance_before_minor, balance_after_minor, created_at)
VALUES
    ('0192f299-0000-7000-8000-000000000020',
     '0192f291-0000-7000-8000-000000000001', '0192f298-0000-7000-8000-000000000010',
     'CREDIT', 100000, 'BRL', 0, 100000, now());

-- Aposta de 25.00 processada, usada como referência das reversões.
INSERT INTO wager_transactions
    (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
     provider_id, external_transaction_id, idempotency_key, payload_hash,
     round_id, game_id, result_balance_minor, created_at, updated_at)
VALUES
    ('0192f298-0000-7000-8000-000000000011', 'EXTERNAL', 'BET', 'PROCESSED',
     '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
     2500, 'BRL', 'provider-a', 'ext-1', 'provider-a:ext-1', '\xaa'::bytea,
     'round-987', 'fortune-chimp', 97500, now(), now());

-- ---------------------------------------------------------------------------
-- Integridade financeira
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'saldo negativo na carteira',
    $cmd$ UPDATE wallets SET balance_minor = -1, version = version + 1
          WHERE id = '0192f291-0000-7000-8000-000000000001' $cmd$);

SELECT assert_rejected(
    'lançamento com balanceAfter incoerente com a direção',
    $cmd$ INSERT INTO wallet_ledger_entries
          (id, wallet_id, transaction_id, direction, amount_minor, currency,
           balance_before_minor, balance_after_minor, created_at)
          VALUES ('0192f299-0000-7000-8000-0000000000f1',
                  '0192f291-0000-7000-8000-000000000001',
                  '0192f298-0000-7000-8000-000000000011',
                  'DEBIT', 2500, 'BRL', 100000, 100000, now()) $cmd$);

SELECT assert_rejected(
    'lançamento com valor zero',
    $cmd$ INSERT INTO wallet_ledger_entries
          (id, wallet_id, transaction_id, direction, amount_minor, currency,
           balance_before_minor, balance_after_minor, created_at)
          VALUES ('0192f299-0000-7000-8000-0000000000f2',
                  '0192f291-0000-7000-8000-000000000001',
                  '0192f298-0000-7000-8000-000000000011',
                  'CREDIT', 0, 'BRL', 100000, 100000, now()) $cmd$);

SELECT assert_rejected(
    'segundo lançamento para a mesma (carteira, transação)',
    $cmd$ INSERT INTO wallet_ledger_entries
          (id, wallet_id, transaction_id, direction, amount_minor, currency,
           balance_before_minor, balance_after_minor, created_at)
          VALUES ('0192f299-0000-7000-8000-0000000000f3',
                  '0192f291-0000-7000-8000-000000000001',
                  '0192f298-0000-7000-8000-000000000010',
                  'CREDIT', 100000, 'BRL', 100000, 200000, now()) $cmd$);

-- ---------------------------------------------------------------------------
-- Ledger append-only
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'UPDATE em lançamento do ledger',
    $cmd$ UPDATE wallet_ledger_entries SET amount_minor = 1
          WHERE id = '0192f299-0000-7000-8000-000000000020' $cmd$);

SELECT assert_rejected(
    'DELETE em lançamento do ledger',
    $cmd$ DELETE FROM wallet_ledger_entries
          WHERE id = '0192f299-0000-7000-8000-000000000020' $cmd$);

SELECT assert_rejected(
    'TRUNCATE no ledger',
    $cmd$ TRUNCATE wallet_ledger_entries $cmd$);

-- ---------------------------------------------------------------------------
-- Estados terminais e imutabilidade
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'transição a partir de estado terminal',
    $cmd$ UPDATE wager_transactions SET status = 'PENDING'
          WHERE id = '0192f298-0000-7000-8000-000000000011' $cmd$);

SELECT assert_rejected(
    'DELETE de transação',
    $cmd$ DELETE FROM wager_transactions
          WHERE id = '0192f298-0000-7000-8000-000000000011' $cmd$);

SELECT assert_rejected(
    'mudança de saldo sem incremento de versão',
    $cmd$ UPDATE wallets SET balance_minor = balance_minor - 100
          WHERE id = '0192f291-0000-7000-8000-000000000001' $cmd$);

SELECT assert_rejected(
    'incremento de versão sem mudança de saldo',
    $cmd$ UPDATE wallets SET version = version + 1
          WHERE id = '0192f291-0000-7000-8000-000000000001' $cmd$);

SELECT assert_rejected(
    'troca de moeda da carteira',
    $cmd$ UPDATE wallets SET currency = 'USD'
          WHERE id = '0192f291-0000-7000-8000-000000000001' $cmd$);

-- ---------------------------------------------------------------------------
-- Idempotência
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'reaplicação de (provider, externalTransactionId) sob outra chave',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000f4', 'EXTERNAL', 'BET', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-1', 'chave-diferente', '\xbb'::bytea,
                  'round-987', 'fortune-chimp', now(), now()) $cmd$);

SELECT assert_rejected(
    'reutilização da chave de idempotência em outra operação',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000f5', 'EXTERNAL', 'BET', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-outro', 'provider-a:ext-1', '\xbb'::bytea,
                  'round-987', 'fortune-chimp', now(), now()) $cmd$);

SELECT assert_rejected(
    'segunda abertura para a mesma carteira',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           result_balance_minor, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000f6', 'INTERNAL', 'OPENING', 'PROCESSED',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  50000, 'BRL', 150000, now(), now()) $cmd$);

SELECT assert_rejected(
    'segunda carteira para o mesmo (jogador, moeda)',
    $cmd$ INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
          VALUES ('0192f291-0000-7000-8000-0000000000f7',
                  '0192f28f-0000-7000-8000-0000000000a1', 'BRL', 0, 1, now(), now()) $cmd$);

-- ---------------------------------------------------------------------------
-- Separação entre origem interna e externa
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'OPENING vindo de provedor externo',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000f8', 'EXTERNAL', 'OPENING', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  10000, 'BRL', 'provider-a', 'ext-opening', 'provider-a:ext-opening',
                  '\xcc'::bytea, 'round-1', 'game-1', now(), now()) $cmd$);

SELECT assert_rejected(
    'abertura interna carregando metadados externos',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000f9', 'INTERNAL', 'OPENING', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  10000, 'BRL', 'provider-a', now(), now()) $cmd$);

SELECT assert_rejected(
    'operação externa sem hash de payload',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000fa', 'EXTERNAL', 'BET', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-sem-hash', 'k', 'r', 'g', now(), now()) $cmd$);

-- ---------------------------------------------------------------------------
-- Política de valor por tipo
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'LOSS com valor diferente de zero',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000fb', 'EXTERNAL', 'LOSS', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  100, 'BRL', 'provider-a', 'ext-loss', 'provider-a:ext-loss', '\xdd'::bytea,
                  'round-1', 'game-1', now(), now()) $cmd$);

SELECT assert_rejected(
    'BET com valor zero',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000fc', 'EXTERNAL', 'BET', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  0, 'BRL', 'provider-a', 'ext-bet0', 'provider-a:ext-bet0', '\xde'::bytea,
                  'round-1', 'game-1', now(), now()) $cmd$);

SELECT assert_rejected(
    'REFUND sem referência externa',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000fd', 'EXTERNAL', 'REFUND', 'PENDING',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-refund', 'provider-a:ext-refund', '\xdf'::bytea,
                  'round-1', 'game-1', now(), now()) $cmd$);

-- ---------------------------------------------------------------------------
-- Reversões: uma por tipo e por referência
-- ---------------------------------------------------------------------------

SELECT assert_accepted(
    'primeiro REFUND sobre a aposta',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, reference_external_transaction_id, reference_transaction_id,
           result_balance_minor, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-000000000012', 'EXTERNAL', 'REFUND', 'PROCESSED',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-2', 'provider-a:ext-2', '\xe0'::bytea,
                  'round-987', 'fortune-chimp', 'ext-1', '0192f298-0000-7000-8000-000000000011',
                  100000, now(), now()) $cmd$);

SELECT assert_rejected(
    'segundo REFUND bem-sucedido sobre a mesma aposta',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, reference_external_transaction_id, reference_transaction_id,
           result_balance_minor, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-000000000013', 'EXTERNAL', 'REFUND', 'PROCESSED',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-3', 'provider-a:ext-3', '\xe1'::bytea,
                  'round-987', 'fortune-chimp', 'ext-1', '0192f298-0000-7000-8000-000000000011',
                  102500, now(), now()) $cmd$);

-- O índice é por (referência, tipo): um ROLLBACK sobre a mesma aposta passa
-- pelo banco. Impedir a devolução dupla de REFUND + ROLLBACK é regra de
-- negócio, aplicada sob o lock da carteira e documentada em ARCHITECTURE.md.
SELECT assert_accepted(
    'ROLLBACK sobre aposta já estornada passa pelo banco (regra é da aplicação)',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, reference_external_transaction_id, reference_transaction_id,
           result_balance_minor, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-000000000014', 'EXTERNAL', 'ROLLBACK', 'PROCESSED',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  2500, 'BRL', 'provider-a', 'ext-4', 'provider-a:ext-4', '\xe2'::bytea,
                  'round-987', 'fortune-chimp', 'ext-1', '0192f298-0000-7000-8000-000000000011',
                  102500, now(), now()) $cmd$);

-- ---------------------------------------------------------------------------
-- Coerência de estado
-- ---------------------------------------------------------------------------

SELECT assert_rejected(
    'PROCESSED sem saldo resultante congelado',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000fe', 'EXTERNAL', 'WIN', 'PROCESSED',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  1000, 'BRL', 'provider-a', 'ext-win', 'provider-a:ext-win', '\xe3'::bytea,
                  'round-1', 'game-1', now(), now()) $cmd$);

SELECT assert_rejected(
    'failureCode em operação bem-sucedida',
    $cmd$ INSERT INTO wager_transactions
          (id, source, kind, status, wallet_id, player_id, amount_minor, currency,
           provider_id, external_transaction_id, idempotency_key, payload_hash,
           round_id, game_id, result_balance_minor, failure_code, created_at, updated_at)
          VALUES ('0192f298-0000-7000-8000-0000000000ff', 'EXTERNAL', 'WIN', 'PROCESSED',
                  '0192f291-0000-7000-8000-000000000001', '0192f28f-0000-7000-8000-0000000000a1',
                  1000, 'BRL', 'provider-a', 'ext-win2', 'provider-a:ext-win2', '\xe4'::bytea,
                  'round-1', 'game-1', 101000, 'INSUFFICIENT_FUNDS', now(), now()) $cmd$);

\echo ''
\echo '================ TODAS AS PROTEÇÕES VERIFICADAS ================'
