// Package metrics define as métricas do serviço.
//
// Os nomes seguem a convenção do Prometheus: prefixo do serviço, unidade no
// sufixo, contadores no plural terminando em _total. As dimensões são
// escolhidas para caber num painel sem explodir a cardinalidade — nada de
// walletId ou transactionId como rótulo, que gerariam uma série por carteira.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics reúne os instrumentos do serviço.
type Metrics struct {
	registry *prometheus.Registry

	// HTTP
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	// Operações
	wagerResults   *prometheus.CounterVec
	wagerDuration  *prometheus.HistogramVec
	wagerReplays   *prometheus.CounterVec
	wagerConflicts prometheus.Counter

	// Concorrência
	lockContention prometheus.Counter

	// Outbox
	outboxPublished prometheus.Counter
	outboxFailures  prometheus.Counter
	outboxPending   prometheus.Gauge
	outboxLag       prometheus.Gauge

	// Mensageria de entrada
	consumerMessages *prometheus.CounterVec

	// Referências pendentes
	referenceOutcomes *prometheus.CounterVec

	// Reconciliação
	reconciliations        *prometheus.CounterVec
	reconciliationMismatch prometheus.Counter
}

// New registra os instrumentos.
func New() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry: registry,

		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jungle_http_requests_total",
			Help: "Requisições HTTP por rota e código de resposta.",
		}, []string{"method", "route", "status"}),

		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "jungle_http_request_duration_seconds",
			Help: "Latência das requisições HTTP.",
			// Os limites cobrem de uma consulta rápida a uma operação que
			// esperou lock: abaixo de 5ms não interessa separar, acima de 2s
			// já é problema.
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 5},
		}, []string{"method", "route"}),

		wagerResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jungle_wager_results_total",
			Help: "Operações por tipo, estado final e código de falha.",
		}, []string{"kind", "status", "failure_code", "source"}),

		wagerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jungle_wager_processing_duration_seconds",
			Help:    "Tempo de processamento de uma operação, do comando ao commit.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 5},
		}, []string{"kind", "source"}),

		wagerReplays: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jungle_wager_replays_total",
			Help: "Reenvios reconhecidos como repetição de uma operação já processada.",
		}, []string{"source"}),

		wagerConflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jungle_wager_idempotency_conflicts_total",
			Help: "Chaves de idempotência reutilizadas com conteúdo diferente.",
		}),

		lockContention: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jungle_wallet_concurrency_conflicts_total",
			Help: "Atualizações de saldo recusadas por versão desatualizada. " +
				"Com o lock pessimista isto deveria ser sempre zero: valor diferente " +
				"indica escrita por um caminho que não passou pelo lock.",
		}),

		outboxPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jungle_outbox_published_total",
			Help: "Eventos de integração publicados com sucesso.",
		}),

		outboxFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jungle_outbox_publish_failures_total",
			Help: "Tentativas de publicação que falharam e foram reagendadas.",
		}),

		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jungle_outbox_pending_events",
			Help: "Eventos aguardando publicação.",
		}),

		outboxLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jungle_outbox_lag_seconds",
			Help: "Idade do evento mais antigo ainda não publicado. " +
				"É a métrica que denuncia um publisher parado.",
		}),

		consumerMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jungle_consumer_messages_total",
			Help: "Mensagens da fila por desfecho. O rótulo permanent distingue " +
				"a falha que seguirá para a DLQ da que será reentregue.",
		}, []string{"outcome", "permanent"}),

		referenceOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jungle_reference_outcomes_total",
			Help: "Desfechos das pendências de referência a cada varredura.",
		}, []string{"outcome"}),

		reconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jungle_reconciliations_total",
			Help: "Reconciliações executadas, por resultado.",
		}, []string{"result"}),

		reconciliationMismatch: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jungle_reconciliation_mismatches_total",
			Help: "Divergências entre o saldo armazenado e o reconstruído pelo ledger. " +
				"Qualquer valor acima de zero exige investigação.",
		}),
	}

	registry.MustRegister(
		m.httpRequests, m.httpDuration,
		m.wagerResults, m.wagerDuration, m.wagerReplays, m.wagerConflicts,
		m.lockContention,
		m.outboxPublished, m.outboxFailures, m.outboxPending, m.outboxLag,
		m.consumerMessages, m.referenceOutcomes,
		m.reconciliations, m.reconciliationMismatch,
	)
	return m
}

// Registry expõe o registrador para o endpoint de coleta.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// ObserveHTTP registra uma requisição atendida.
func (m *Metrics) ObserveHTTP(method, route, status string, seconds float64) {
	m.httpRequests.WithLabelValues(method, route, status).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(seconds)
}

// ObserveWager registra o desfecho de uma operação.
//
// failureCode entra vazio quando não houve recusa; manter o rótulo presente
// evita que a série mude de forma entre sucesso e falha.
func (m *Metrics) ObserveWager(kind, status, failureCode, source string, seconds float64) {
	m.wagerResults.WithLabelValues(kind, status, failureCode, source).Inc()
	m.wagerDuration.WithLabelValues(kind, source).Observe(seconds)
}

// ObserveReplay registra um reenvio reconhecido.
func (m *Metrics) ObserveReplay(source string) { m.wagerReplays.WithLabelValues(source).Inc() }

// ObserveIdempotencyConflict registra uma chave reutilizada com outro conteúdo.
func (m *Metrics) ObserveIdempotencyConflict() { m.wagerConflicts.Inc() }

// ObserveConcurrencyConflict registra uma atualização recusada por versão.
func (m *Metrics) ObserveConcurrencyConflict() { m.lockContention.Inc() }

// ObserveOutboxPublish registra o resultado de uma publicação.
func (m *Metrics) ObserveOutboxPublish(sucesso bool) {
	if sucesso {
		m.outboxPublished.Inc()
		return
	}
	m.outboxFailures.Inc()
}

// ObserveOutboxBacklog atualiza o tamanho e o atraso da fila de publicação.
func (m *Metrics) ObserveOutboxBacklog(pendentes int, atrasoSegundos float64) {
	m.outboxPending.Set(float64(pendentes))
	m.outboxLag.Set(atrasoSegundos)
}

// ObserveConsumerMessage registra o desfecho de uma mensagem.
func (m *Metrics) ObserveConsumerMessage(outcome string, permanent bool) {
	rotulo := "false"
	if permanent {
		rotulo = "true"
	}
	m.consumerMessages.WithLabelValues(outcome, rotulo).Inc()
}

// ObserveReferenceOutcome registra o desfecho de uma pendência.
func (m *Metrics) ObserveReferenceOutcome(outcome string) {
	m.referenceOutcomes.WithLabelValues(outcome).Inc()
}

// ObserveReconciliation registra uma reconciliação e, se houver, a divergência.
func (m *Metrics) ObserveReconciliation(consistente bool) {
	if consistente {
		m.reconciliations.WithLabelValues("consistent").Inc()
		return
	}
	m.reconciliations.WithLabelValues("divergent").Inc()
	m.reconciliationMismatch.Inc()
}
