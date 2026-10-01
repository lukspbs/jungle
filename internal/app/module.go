package app

import (
	"go.uber.org/fx"

	"github.com/lukspbs/jungle/internal/adapter/postgres"

	"github.com/lukspbs/jungle/internal/platform/config"
)

// Module provê os casos de uso.
//
// Clock e IDs são providos como interfaces para que os testes substituam o
// relógio e o gerador sem tocar em nenhum caso de uso.
var Module = fx.Module("app",
	fx.Provide(
		func() Clock { return SystemClock{} },
		func() IDs { return UUIDv7{} },
		referencePolicyFrom,

		NewOpenWallet,
		NewProcessWager,
		NewQueries,
		NewReadiness,
		newReferenceWorker,
		newOutboxPublisher,
	),
)

func referencePolicyFrom(cfg config.Reference) ReferencePolicy {
	return ReferencePolicy{
		TTL:            cfg.TTL,
		MaxAttempts:    cfg.MaxAttempts,
		InitialBackoff: cfg.InitialBackoff,
		MaxBackoff:     cfg.MaxBackoff,
	}
}

func newReferenceWorker(
	processar *ProcessWager, store *postgres.Store, clock Clock,
	policy ReferencePolicy, cfg config.Reference,
) *ReferenceWorker {
	return NewReferenceWorker(processar, store, clock, policy, cfg.BatchSize, cfg.PollInterval)
}

func newOutboxPublisher(
	store *postgres.Store, publisher EventPublisher, clock Clock,
	app config.App, cfg config.Outbox,
) *OutboxPublisher {
	return NewOutboxPublisher(store, publisher, clock, app.InstanceID,
		cfg.BatchSize, cfg.Lease, cfg.PollInterval, cfg.InitialBackoff, cfg.MaxBackoff)
}
