package worker

import (
	"go.uber.org/fx"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// Module registra os workers no ciclo de vida.
//
// Eles são registrados depois do servidor HTTP de propósito. O Fx encerra na
// ordem inversa, então o servidor para de aceitar requisições primeiro e os
// workers só então são interrompidos — o que evita interromper um worker
// enquanto ainda chega trabalho novo por HTTP.
var Module = fx.Module("worker",
	fx.Invoke(
		registerReferenceWorker,
		registerOutboxPublisher,
	),
)

func registerReferenceWorker(
	lc fx.Lifecycle, w *app.ReferenceWorker, cfg config.HTTP,
) {
	runner := New("reference-worker", w, cfg.ShutdownTimeout)
	lc.Append(fx.Hook{OnStart: runner.Start, OnStop: runner.Stop})
}

func registerOutboxPublisher(
	lc fx.Lifecycle, p *app.OutboxPublisher, cfg config.HTTP,
) {
	runner := New("outbox-publisher", p, cfg.ShutdownTimeout)
	lc.Append(fx.Hook{OnStart: runner.Start, OnStop: runner.Stop})
}
