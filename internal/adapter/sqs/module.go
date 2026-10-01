package sqs

import (
	"context"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// Module provê o cliente e o publicador.
var Module = fx.Module("sqs",
	fx.Provide(
		newClient,
		NewPublisher,
		func(p *Publisher) app.EventPublisher { return p },
	),
)

func newClient(cfg config.SQS) (*awssqs.Client, error) {
	return NewClient(context.Background(), cfg)
}
