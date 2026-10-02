package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/platform/logging"
)

// correlationHeader carrega o identificador que amarra a operação ponta a
// ponta: logs, eventos e a resposta ao cliente.
const correlationHeader = "X-Correlation-Id"

type contextKey string

const correlationKey contextKey = "correlationId"

// correlationID devolve o identificador da requisição.
func correlationID(r *http.Request) string {
	if v, ok := r.Context().Value(correlationKey).(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// writeJSON escreve a resposta.
func writeJSON(w http.ResponseWriter, status int, corpo any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// O erro aqui significa conexão perdida depois do cabeçalho: não há mais
	// como informar o cliente, e o log da requisição já registra o desfecho.
	_ = json.NewEncoder(w).Encode(corpo)
}

// writeError traduz o erro e responde.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := statusFor(err)
	mensagem := err.Error()
	if status == http.StatusInternalServerError {
		// Um erro inesperado pode carregar detalhe interno — nome de
		// constraint, trecho de SQL. O cliente recebe o correlationId e o
		// operador encontra o resto no log.
		mensagem = "erro interno ao processar a requisição"
	}
	writeProblem(w, r, status, code, mensagem)
}

// writeProblem responde com o corpo de erro do contrato.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, mensagem string) {
	writeJSON(w, status, problem{
		Code:          code,
		Message:       mensagem,
		CorrelationID: correlationID(r),
	})
}

func rfc3339(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// transactionOf monta a resposta de consulta de uma transação.
func transactionOf(tx *wagering.WagerTransaction) transactionResponse {
	out := transactionResponse{
		ID:                             tx.ID(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		WalletID:                       tx.WalletID(),
		PlayerID:                       tx.PlayerID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           tx.Kind().String(),
		Status:                         tx.Status().String(),
		Money:                          tx.Amount(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		CreatedAt:                      rfc3339(tx.CreatedAt()),
		UpdatedAt:                      rfc3339(tx.UpdatedAt()),
	}
	if saldo := tx.ResultBalance(); !saldo.IsUninitialized() {
		out.Balance = &saldo
	}
	if code := tx.FailureCode(); code != "" {
		corrigivel := code.Correctable()
		out.FailureCode = code.String()
		out.Correctable = &corrigivel
	}
	return out
}

// WithCorrelationID garante que toda requisição tenha um identificador.
//
// O cliente pode informar o seu; quando não informa, o servidor gera. Em ambos
// os casos ele volta no cabeçalho da resposta, para que o cliente consiga
// correlacionar sem adivinhar.
func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(correlationHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, id)))
	})
}

// Recover transforma um panic em resposta 500.
//
// Um panic num handler derrubaria a conexão sem resposta e, sem isto, poderia
// derrubar o processo inteiro. As rejeições de negócio nunca chegam aqui: elas
// são erros devolvidos, não panics.
//
// O valor recuperado e a pilha vão para o log. Sem isso o panic virava um 500
// silencioso: o operador via o código de resposta e não tinha como descobrir de
// onde ele veio — e panic é exatamente o caso em que a pilha é a única pista.
// O cliente continua recebendo só o correlationId, porque a pilha descreve o
// interior do processo.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recuperado := recover()
				if recuperado == nil {
					return
				}
				logger.ErrorContext(r.Context(), "panic no handler",
					slog.Any("panic", recuperado),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String(logging.FieldCorrelationID, correlationID(r)),
					slog.String("stack", string(debug.Stack())))
				writeError(w, r, errors.New("panic no handler"))
			}()
			next.ServeHTTP(w, r)
		})
	}
}
