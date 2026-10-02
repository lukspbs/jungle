package http

import (
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lukspbs/jungle/internal/platform/auth"
	"github.com/lukspbs/jungle/internal/platform/metrics"
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
func NewRouter(
	h *Handlers, verifier *auth.Verifier, logger *slog.Logger, m *metrics.Metrics,
) http.Handler {
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

	// Coleta de métricas. Público como os health checks, pela mesma razão: um
	// coletor dentro do cluster não carrega credencial de provedor. Num
	// ambiente real este caminho fica atrás de política de rede ou numa porta
	// administrativa separada — a observação está no README.
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{}))

	// A ordem: a recuperação de panic por fora, para que qualquer falha vire
	// resposta; o correlationId logo dentro, para que toda linha de log e toda
	// resposta de erro o carreguem; o log em seguida, para registrar também as
	// requisições recusadas na autenticação; e a autenticação por último, mais
	// perto dos handlers.
	return Recover(WithCorrelationID(LogRequests(logger, m)(Authenticate(verifier)(mux))))
}
