package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/lukspbs/jungle/internal/platform/logging"
	"github.com/lukspbs/jungle/internal/platform/metrics"
)

// statusRecorder captura o código de resposta para o log.
//
// O http.ResponseWriter não expõe o status depois de escrito, então a única
// forma de registrá-lo é interceptar a escrita.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// LogRequests registra uma linha por requisição.
//
// O corpo não é registrado. Ele carrega valores monetários e identificadores de
// jogador, e espalhá-los pelo coletor de logs os tira do controle de acesso que
// o banco tem. O que vai para a linha é o suficiente para rastrear: método,
// rota, desfecho, duração e o correlationId.
//
// A rota registrada é o padrão casado, não o caminho literal: agrupar por
// "/wallets/{walletId}" permite medir latência por endpoint, o que o caminho
// com o id de cada carteira não permitiria.
func LogRequests(logger *slog.Logger, m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inicio := time.Now()

			ctx := logging.Into(r.Context(), logger)
			ctx = logging.With(ctx, logging.FieldCorrelationID, correlationID(r.WithContext(ctx)))
			r = r.WithContext(ctx)

			gravador := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(gravador, r)

			duracao := time.Since(inicio)
			m.ObserveHTTP(r.Method, rota(r), strconv.Itoa(gravador.status), duracao.Seconds())

			atributos := []any{
				slog.String("method", r.Method),
				slog.String("route", rota(r)),
				slog.Int("status", gravador.status),
				slog.Int64(logging.FieldDurationMs, duracao.Milliseconds()),
			}
			if id, ok := identityOf(r); ok {
				atributos = append(atributos, slog.String("client", id.ClientID))
				if id.ProviderID != "" {
					atributos = append(atributos, slog.String(logging.FieldProviderID, id.ProviderID))
				}
			}

			registrador := logging.From(r.Context())
			switch {
			case gravador.status >= 500:
				registrador.ErrorContext(r.Context(), "requisição falhou", atributos...)
			case gravador.status >= 400:
				registrador.WarnContext(r.Context(), "requisição recusada", atributos...)
			default:
				registrador.InfoContext(r.Context(), "requisição atendida", atributos...)
			}
		})
	}
}

// rota devolve o padrão casado pelo mux, com o caminho literal como recurso.
func rota(r *http.Request) string {
	if p := r.Pattern; p != "" {
		return p
	}
	return r.Method + " " + r.URL.Path
}
