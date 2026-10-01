package app

import "errors"

// Erros da camada de aplicação.
//
// Eles existem para que a borda HTTP escolha o código de resposta sem precisar
// interpretar erro de domínio nem de driver. A tradução acontece uma vez, aqui.
var (
	// ErrInvalidCommand indica comando malformado antes de qualquer efeito.
	ErrInvalidCommand = errors.New("app: comando inválido")

	// ErrWalletAlreadyExists indica abertura repetida para o par
	// (jogador, moeda).
	ErrWalletAlreadyExists = errors.New("app: carteira já existe para este jogador e moeda")

	// ErrWalletNotFound indica carteira inexistente.
	ErrWalletNotFound = errors.New("app: carteira não encontrada")

	// ErrTransactionNotFound indica transação inexistente.
	ErrTransactionNotFound = errors.New("app: transação não encontrada")

	// ErrIdempotencyConflict indica chave reutilizada com conteúdo diferente.
	ErrIdempotencyConflict = errors.New("app: chave de idempotência reutilizada com outro conteúdo")

	// ErrUnavailable indica indisponibilidade transitória de uma dependência.
	// É o que distingue "tente de novo" de "não insista".
	ErrUnavailable = errors.New("app: dependência temporariamente indisponível")
)
