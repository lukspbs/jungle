package http

import (
	"net/http"

	"github.com/lukspbs/jungle/internal/platform/auth"
)

// NewRouter monta o roteamento.
//
// Uso o ServeMux da biblioteca padrão, que desde o Go 1.22 reconhece método e
// variáveis de caminho. Um roteador externo não traria nada que este contrato
// precise, e traria uma dependência a mais para justificar.
//
// A ordem dos middlewares importa: a recuperação de panic fica por fora, para
// que um panic no próprio middleware de correlação ainda vire resposta; o
// correlationId fica logo dentro, para que todo log e toda resposta de erro o
// tenham.
func NewRouter(h *Handlers, verifier *auth.Verifier) http.Handler {
	mux := http.NewServeMux()

	// Carteiras.
	mux.HandleFunc("POST /wallets", h.OpenWallet)
	mux.HandleFunc("GET /wallets/{walletId}", h.GetWallet)
	mux.HandleFunc("GET /wallets/{walletId}/ledger", h.GetLedger)
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", h.Reconcile)

	// Operações.
	mux.HandleFunc("POST /wagering/transactions", h.ProcessWager)
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", h.GetTransaction)
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}",
		h.GetProviderTransaction)

	// Saúde. Os únicos caminhos públicos: um verificador de readiness de
	// orquestrador não tem credencial, e exigir uma transformaria
	// indisponibilidade do IdP em indisponibilidade aparente do serviço.
	mux.HandleFunc("GET /health/live", h.Live)
	mux.HandleFunc("GET /health/ready", h.Ready)

	// A autenticação fica dentro do correlationId para que a resposta 401
	// também o carregue, e dentro da recuperação de panic pelo mesmo motivo.
	return Recover(WithCorrelationID(Authenticate(verifier)(mux)))
}
