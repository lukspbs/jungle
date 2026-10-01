// Package http expõe a API do serviço.
//
// O pacote traduz entre o contrato externo e os casos de uso: decodifica,
// valida forma, chama a aplicação e escolhe o código de resposta. Nenhuma regra
// de negócio mora aqui.
package http

import (
	"errors"
	"net/http"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
)

// problem é o corpo de toda resposta de erro.
//
// O código é estável e legível por máquina; a mensagem é para humanos. As duas
// coisas separadas evitam que um cliente passe a depender do texto.
type problem struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId,omitempty"`

	// Preenchidos quando o erro é uma recusa de negócio já registrada.
	TransactionID string `json:"transactionId,omitempty"`
	FailureCode   string `json:"failureCode,omitempty"`
	Correctable   *bool  `json:"correctable,omitempty"`
}

// Códigos de erro do contrato.
const (
	codeInvalidRequest      = "INVALID_REQUEST"
	codeMissingIdempotency  = "MISSING_IDEMPOTENCY_KEY"
	codeNotFound            = "NOT_FOUND"
	codeConflict            = "CONFLICT"
	codeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	codeBusinessRejection   = "BUSINESS_REJECTION"
	codeUnavailable         = "SERVICE_UNAVAILABLE"
	codeInternal            = "INTERNAL_ERROR"
)

// statusFor traduz um erro da aplicação em código HTTP e código de contrato.
//
// O mapa é a documentação executável do §9: entrada inválida, conflito,
// rejeição de negócio, processamento pendente e indisponibilidade transitória
// são cinco situações distintas, e o cliente precisa distingui-las sem ler o
// texto da mensagem.
//
//	400  entrada malformada, campo ausente, valor fora do contrato
//	404  carteira ou transação inexistente
//	409  abertura repetida, ou chave de idempotência com outro conteúdo
//	422  recusa por regra de negócio, já registrada e com failureCode
//	503  dependência temporariamente indisponível; vale tentar de novo
//	500  o resto
//
// Processamento pendente não é erro: sai como 202 no caminho de sucesso.
func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, app.ErrInvalidCommand),
		errors.Is(err, money.ErrInvalidAmount),
		errors.Is(err, money.ErrScaleExceeded),
		errors.Is(err, money.ErrNegativeAmount),
		errors.Is(err, money.ErrInvalidCurrency),
		errors.Is(err, money.ErrOverflow),
		errors.Is(err, wagering.ErrInvalidKind),
		errors.Is(err, wagering.ErrKindNotAllowed),
		errors.Is(err, wagering.ErrAmountPolicy),
		errors.Is(err, wagering.ErrReferencePolicy),
		errors.Is(err, wagering.ErrMissingField):
		return http.StatusBadRequest, codeInvalidRequest

	case errors.Is(err, app.ErrWalletNotFound), errors.Is(err, app.ErrTransactionNotFound):
		return http.StatusNotFound, codeNotFound

	case errors.Is(err, app.ErrWalletAlreadyExists):
		return http.StatusConflict, codeConflict

	case errors.Is(err, app.ErrIdempotencyConflict):
		return http.StatusConflict, codeIdempotencyConflict

	case errors.Is(err, app.ErrUnavailable):
		return http.StatusServiceUnavailable, codeUnavailable

	default:
		return http.StatusInternalServerError, codeInternal
	}
}
