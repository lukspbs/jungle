package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/wagering"
)

// TransactionRepository persiste as transações de aposta.
type TransactionRepository struct {
	db DBTX
}

const transactionColumns = `id, source, kind, status, wallet_id, player_id,
	amount_minor, currency,
	provider_id, external_transaction_id, idempotency_key, payload_hash,
	round_id, game_id,
	reference_external_transaction_id, reference_transaction_id,
	result_balance_minor, failure_code,
	created_at, updated_at`

// Insert grava uma transação recém-aceita.
//
// A inserção é o ponto de idempotência: em vez de consultar antes para ver se
// a operação já existe — o que abre janela entre a consulta e a escrita —
// tento inserir e deixo a constraint decidir. O erro nomeado que volta
// diz exatamente qual garantia foi tocada.
func (r *TransactionRepository) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	const query = `
		INSERT INTO wager_transactions (` + transactionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`

	_, err := r.db.Exec(ctx, query,
		t.ID(), t.Source().String(), t.Kind().String(), t.Status().String(),
		t.WalletID(), t.PlayerID(),
		t.Amount().MinorUnits(), t.Amount().Currency().String(),
		nullableText(t.ProviderID()), nullableText(t.ExternalTransactionID()),
		nullableText(t.IdempotencyKey()), nullableBytes(t.PayloadHash()),
		nullableText(t.RoundID()), nullableText(t.GameID()),
		nullableText(t.ReferenceExternalTransactionID()),
		nullableUUID(t.ReferenceTransactionID()),
		fromOptionalMoney(t.ResultBalance()), nullableText(t.FailureCode().String()),
		t.CreatedAt(), t.UpdatedAt())
	if err != nil {
		return fmt.Errorf("postgres: falha ao inserir transação: %w", classify(err))
	}
	return nil
}

// Update grava o desfecho de uma transação.
//
// Identidade e valor não entram no UPDATE: são imutáveis depois do aceite, e a
// trigger do banco recusa alterá-los. Só muda o que é resultado.
func (r *TransactionRepository) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	const query = `
		UPDATE wager_transactions
		   SET status = $1,
		       reference_transaction_id = $2,
		       result_balance_minor = $3,
		       failure_code = $4,
		       updated_at = $5
		 WHERE id = $6`

	tag, err := r.db.Exec(ctx, query,
		t.Status().String(), nullableUUID(t.ReferenceTransactionID()),
		fromOptionalMoney(t.ResultBalance()), nullableText(t.FailureCode().String()),
		t.UpdatedAt(), t.ID())
	if err != nil {
		return fmt.Errorf("postgres: falha ao atualizar transação: %w", classify(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrTransactionNotFound, t.ID())
	}
	return nil
}

// FindByID lê uma transação pelo identificador interno.
func (r *TransactionRepository) FindByID(
	ctx context.Context, id uuid.UUID,
) (*wagering.WagerTransaction, error) {
	const query = `SELECT ` + transactionColumns + ` FROM wager_transactions WHERE id = $1`
	return r.scanOne(ctx, query, id)
}

// FindByProviderAndExternalID resolve a operação por (provedor, id externo).
//
// É a consulta que resolve referências de reversão e a que sustenta o
// isolamento entre provedores: a busca é sempre escopada pelo provedor
// autenticado, nunca só pelo id externo.
func (r *TransactionRepository) FindByProviderAndExternalID(
	ctx context.Context, providerID, externalTransactionID string,
) (*wagering.WagerTransaction, error) {
	const query = `
		SELECT ` + transactionColumns + `
		  FROM wager_transactions
		 WHERE provider_id = $1 AND external_transaction_id = $2`
	return r.scanOne(ctx, query, providerID, externalTransactionID)
}

// FindByProviderAndIdempotencyKey resolve a operação pela chave recebida.
func (r *TransactionRepository) FindByProviderAndIdempotencyKey(
	ctx context.Context, providerID, idempotencyKey string,
) (*wagering.WagerTransaction, error) {
	const query = `
		SELECT ` + transactionColumns + `
		  FROM wager_transactions
		 WHERE provider_id = $1 AND idempotency_key = $2`
	return r.scanOne(ctx, query, providerID, idempotencyKey)
}

// FindProcessedReversal devolve a reversão bem-sucedida que já incide sobre uma
// referência, se houver.
//
// O índice único do banco impede duas reversões do mesmo tipo. Esta consulta
// cobre o caso que ele não cobre: um REFUND e um ROLLBACK sobre a mesma aposta
// são tipos diferentes, passariam pelo índice, e devolveriam o débito duas
// vezes. A verificação acontece sob o lock da carteira, então não há janela
// entre consultar e decidir.
func (r *TransactionRepository) FindProcessedReversal(
	ctx context.Context, referenceTransactionID uuid.UUID,
) (*wagering.WagerTransaction, error) {
	const query = `
		SELECT ` + transactionColumns + `
		  FROM wager_transactions
		 WHERE reference_transaction_id = $1
		   AND status = 'PROCESSED'
		   AND kind IN ('REFUND', 'ROLLBACK')
		 LIMIT 1`
	return r.scanOne(ctx, query, referenceTransactionID)
}

// ClaimPendingReferences reivindica operações cuja referência ainda não chegou.
//
// SKIP LOCKED permite que várias instâncias do worker dividam a fila. A
// reivindicação já agenda a próxima tentativa, de modo que uma instância que
// morra no meio não prenda o registro: ele volta a ficar elegível quando o
// prazo recém-gravado vencer.
//
// É aqui, e só aqui, que reference_attempts avança. Contar na reivindicação e
// não no desfecho é deliberado: uma tentativa que nunca retorna precisa contar,
// senão uma pendência problemática seria retomada para sempre.
//
// O UPDATE devolve só os identificadores, e cada transação é relida pelo
// caminho normal. Repetir aqui a montagem do snapshot duplicaria vinte colunas
// e criaria um segundo lugar para a reidratação sair de sincronia com o schema.
func (r *TransactionRepository) ClaimPendingReferences(
	ctx context.Context, limit int, now, nextAttemptAt time.Time,
) ([]*wagering.WagerTransaction, error) {
	const query = `
		UPDATE wager_transactions
		   SET reference_attempts = reference_attempts + 1,
		       reference_next_attempt_at = $1
		 WHERE id IN (
		       SELECT id FROM wager_transactions
		        WHERE status = 'PENDING_REFERENCE'
		          AND (reference_next_attempt_at IS NULL OR reference_next_attempt_at <= $2)
		        ORDER BY reference_next_attempt_at NULLS FIRST
		        LIMIT $3
		        FOR UPDATE SKIP LOCKED
		 )
		 RETURNING id`

	rows, err := r.db.Query(ctx, query, nextAttemptAt, now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao reivindicar pendências: %w", classify(err))
	}

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("postgres: falha ao ler pendência: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pendentes := make([]*wagering.WagerTransaction, 0, len(ids))
	for _, id := range ids {
		tx, err := r.FindByID(ctx, id)
		if err != nil {
			return nil, err
		}
		pendentes = append(pendentes, tx)
	}
	return pendentes, nil
}

// ReferenceExpiry devolve o prazo e as tentativas já gastas de uma pendência.
func (r *TransactionRepository) ReferenceExpiry(
	ctx context.Context, id uuid.UUID,
) (attempts int, expiresAt time.Time, err error) {
	const query = `
		SELECT reference_attempts, COALESCE(reference_expires_at, 'infinity'::timestamptz)
		  FROM wager_transactions WHERE id = $1`
	if err := r.db.QueryRow(ctx, query, id).Scan(&attempts, &expiresAt); err != nil {
		if noRows(err) {
			return 0, time.Time{}, ErrTransactionNotFound
		}
		return 0, time.Time{}, fmt.Errorf("postgres: falha ao ler prazos da pendência: %w", classify(err))
	}
	return attempts, expiresAt, nil
}

// ScheduleReferenceRetry registra a próxima tentativa do worker de referências
// pendentes.
//
// As colunas de tentativa ficam fora do agregado de propósito: elas descrevem o
// agendamento do worker, não o estado financeiro da operação. Misturá-las no
// domínio faria a transação carregar detalhe de infraestrutura.
//
// O contador não é tocado aqui. Ele pertence a ClaimPendingReferences, que é
// onde uma tentativa de fato começa — incrementar nos dois lugares faria cada
// rodada do worker contar duas, e o REFERENCE_MAX_ATTEMPTS configurado valeria
// metade. Este método só agenda: quando a próxima tentativa acontece e até
// quando a espera vale.
func (r *TransactionRepository) ScheduleReferenceRetry(
	ctx context.Context, id uuid.UUID, nextAttemptAt, expiresAt time.Time,
) error {
	const query = `
		UPDATE wager_transactions
		   SET reference_next_attempt_at = $1,
		       reference_expires_at = COALESCE(reference_expires_at, $2)
		 WHERE id = $3`

	tag, err := r.db.Exec(ctx, query, nextAttemptAt, expiresAt, id)
	if err != nil {
		return fmt.Errorf("postgres: falha ao agendar nova tentativa: %w", classify(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrTransactionNotFound, id)
	}
	return nil
}

func (r *TransactionRepository) scanOne(
	ctx context.Context, query string, args ...any,
) (*wagering.WagerTransaction, error) {
	var (
		snapshot     wagering.Snapshot
		source       string
		kind         string
		status       string
		currencyCode string
		amountMinor  int64

		providerID     *string
		externalTxID   *string
		idempotencyKey *string
		roundID        *string
		gameID         *string
		referenceExtID *string
		failureCode    *string
		referenceTxID  *uuid.UUID
		resultBalance  *int64
		payloadHash    []byte
	)

	err := r.db.QueryRow(ctx, query, args...).Scan(
		&snapshot.ID, &source, &kind, &status, &snapshot.WalletID, &snapshot.PlayerID,
		&amountMinor, &currencyCode,
		&providerID, &externalTxID, &idempotencyKey, &payloadHash,
		&roundID, &gameID,
		&referenceExtID, &referenceTxID,
		&resultBalance, &failureCode,
		&snapshot.CreatedAt, &snapshot.UpdatedAt)
	if noRows(err) {
		return nil, ErrTransactionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao ler transação: %w", classify(err))
	}

	// source não é reidratada: o domínio a deriva do tipo. Ler a coluna e
	// ignorá-la é deliberado — ela existe no schema para que as constraints
	// condicionais possam se apoiar nela.
	_ = source

	if snapshot.Kind, err = wagering.ParseKind(kind); err != nil {
		return nil, err
	}
	if snapshot.Status, err = wagering.ParseStatus(status); err != nil {
		return nil, err
	}
	if snapshot.Amount, err = toMoney(amountMinor, currencyCode); err != nil {
		return nil, err
	}
	if snapshot.ResultBalance, err = toOptionalMoney(resultBalance, currencyCode); err != nil {
		return nil, err
	}

	snapshot.ProviderID = text(providerID)
	snapshot.ExternalTransactionID = text(externalTxID)
	snapshot.IdempotencyKey = text(idempotencyKey)
	snapshot.PayloadHash = payloadHash
	snapshot.RoundID = text(roundID)
	snapshot.GameID = text(gameID)
	snapshot.ReferenceExternalTransactionID = text(referenceExtID)
	snapshot.ReferenceTransactionID = uuidOrNil(referenceTxID)
	snapshot.FailureCode = wagering.FailureCode(text(failureCode))

	return wagering.Rehydrate(snapshot)
}
