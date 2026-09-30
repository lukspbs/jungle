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
// a operação já existe — o que abre janela entre a consulta e a escrita — a
// gente tenta inserir e deixa a constraint decidir. O erro nomeado que volta
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

// ScheduleReferenceRetry registra a próxima tentativa do worker de referências
// pendentes.
//
// As colunas de tentativa ficam fora do agregado de propósito: elas descrevem o
// agendamento do worker, não o estado financeiro da operação. Misturá-las no
// domínio faria a transação carregar detalhe de infraestrutura.
func (r *TransactionRepository) ScheduleReferenceRetry(
	ctx context.Context, id uuid.UUID, nextAttemptAt, expiresAt time.Time,
) error {
	const query = `
		UPDATE wager_transactions
		   SET reference_attempts = reference_attempts + 1,
		       reference_next_attempt_at = $1,
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
