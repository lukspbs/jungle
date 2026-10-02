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
// A ordem dos middlewares importa, e a razão de cada posição está junto da
// composição no fim desta função.
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

	// A ordem, de fora para dentro:
	//
	// O correlationId primeiro, porque todos os outros dependem dele: o log, a
	// mensagem de panic e o corpo de qualquer resposta de erro o carregam.
	// Tê-lo por dentro da recuperação de panic deixava o 500 de um panic sair
	// com "unknown" no corpo enquanto o header trazia o id real.
	//
	// O log em seguida, para registrar também o que a autenticação recusa.
	//
	// A recuperação de panic depois do log, e não antes: ela transforma o panic
	// em 500 e devolve normalmente, então o log registra a requisição com o
	// status que o cliente recebeu de fato. Por fora, o panic desenrolaria por
	// cima do log. A troca é deixar um panic nos dois middlewares de cima sem
	// resposta — eles não fazem nada que estoure, e o servidor da biblioteca
	// padrão ainda isola a conexão.
	//
	// A autenticação por último, mais perto dos handlers.
	return WithCorrelationID(LogRequests(logger, m)(Recover(logger)(Authenticate(verifier)(mux))))
}
