package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Erros da camada de persistência, classificáveis por errors.Is.
//
// Cada violação de unicidade vira um erro nomeado em vez de um genérico: o
// caso de uso precisa distinguir "essa operação já existe" de "essa chave já
// foi usada em outra operação" para responder replay ou conflito.
var (
	// ErrWalletNotFound indica carteira inexistente.
	ErrWalletNotFound = errors.New("postgres: carteira não encontrada")

	// ErrWalletAlreadyExists indica segunda carteira para o mesmo par
	// (jogador, moeda).
	ErrWalletAlreadyExists = errors.New("postgres: carteira já existe para este jogador e moeda")

	// ErrTransactionNotFound indica transação inexistente.
	ErrTransactionNotFound = errors.New("postgres: transação não encontrada")

	// ErrDuplicateExternalTransaction indica que (provider, externalTransactionId)
	// já existe. É a garantia de que a mesma operação financeira não é
	// reaplicada sob outra chave de idempotência.
	ErrDuplicateExternalTransaction = errors.New("postgres: operação já registrada para este provedor")

	// ErrDuplicateIdempotencyKey indica reutilização da chave de idempotência
	// em outra operação.
	ErrDuplicateIdempotencyKey = errors.New("postgres: chave de idempotência já utilizada")

	// ErrDuplicateOpening indica segunda abertura para a mesma carteira.
	ErrDuplicateOpening = errors.New("postgres: carteira já possui abertura")

	// ErrDuplicateReversal indica segunda reversão bem-sucedida do mesmo tipo
	// sobre a mesma referência.
	ErrDuplicateReversal = errors.New("postgres: referência já estornada por este tipo")

	// ErrDuplicateLedgerEntry indica segundo lançamento para o par
	// (carteira, transação). É a rede final contra movimentação duplicada.
	ErrDuplicateLedgerEntry = errors.New("postgres: lançamento já existe para esta transação")

	// ErrConcurrentUpdate indica que a versão esperada não estava mais lá:
	// outro escritor confirmou uma atualização no intervalo.
	ErrConcurrentUpdate = errors.New("postgres: atualização concorrente detectada")

	// ErrLedgerImmutable indica tentativa de alterar ou apagar um lançamento.
	ErrLedgerImmutable = errors.New("postgres: o ledger é append-only")

	// ErrTerminalTransaction indica tentativa de transicionar uma transação
	// já terminal, barrada pela trigger do banco.
	ErrTerminalTransaction = errors.New("postgres: transação em estado terminal")

	// ErrInvariantViolation indica que uma CHECK constraint recusou a escrita.
	// O banco segurou o que o domínio deveria ter barrado antes: é a rede de
	// segurança funcionando, e ao mesmo tempo o sinal de um bug na aplicação.
	ErrInvariantViolation = errors.New("postgres: invariante do banco violada")
)

// Nomes das constraints do schema. Mantê-los em constantes deixa explícito que
// o mapeamento de erro depende do schema: renomear uma constraint sem ajustar
// aqui quebra a distinção entre replay e conflito.
const (
	constraintWalletPlayerCurrency = "wallets_player_currency_uk"
	constraintProviderExternal     = "wager_transactions_provider_external_uk"
	constraintProviderIdempotency  = "wager_transactions_provider_idempotency_uk"
	constraintSingleOpening        = "wager_transactions_single_opening_uk"
	constraintSingleReversal       = "wager_transactions_single_reversal_uk"
	constraintLedgerWalletTx       = "wallet_ledger_entries_wallet_transaction_uk"
)

// Códigos SQLSTATE relevantes.
const (
	sqlStateUniqueViolation   = "23505"
	sqlStateCheckViolation    = "23514"
	sqlStateRestrictViolation = "23001"
	sqlStateRaiseException    = "P0001"
)

// classify traduz o erro do driver para um erro nomeado do pacote.
//
// A tradução acontece aqui e não no caso de uso: o domínio não deve precisar
// conhecer SQLSTATE nem nome de índice.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case sqlStateUniqueViolation:
		switch pgErr.ConstraintName {
		case constraintWalletPlayerCurrency:
			return ErrWalletAlreadyExists
		case constraintProviderExternal:
			return ErrDuplicateExternalTransaction
		case constraintProviderIdempotency:
			return ErrDuplicateIdempotencyKey
		case constraintSingleOpening:
			return ErrDuplicateOpening
		case constraintSingleReversal:
			return ErrDuplicateReversal
		case constraintLedgerWalletTx:
			return ErrDuplicateLedgerEntry
		}

	case sqlStateRestrictViolation, sqlStateRaiseException:
		// As triggers de proteção levantam exceção com estas mensagens.
		switch {
		case strings.Contains(pgErr.Message, "append-only"):
			return ErrLedgerImmutable
		case strings.Contains(pgErr.Message, "estado terminal"):
			return ErrTerminalTransaction
		}

	case sqlStateCheckViolation:
		// Chegar aqui significa que a aplicação tentou gravar algo que o
		// domínio deveria ter barrado. A constraint segurou, mas é um bug:
		// vale um erro próprio para que apareça distinto nos logs.
		return ErrInvariantViolation
	}

	return err
}
