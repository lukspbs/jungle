// Package sqs implementa a mensageria sobre AWS SQS.
package sqs

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/lukspbs/jungle/internal/platform/config"
)

// NewClient monta o cliente SQS.
//
// Quando um endpoint é informado, ele aponta para o emulador local; vazio, o
// SDK resolve o endpoint real da AWS pela região. A mesma configuração serve
// aos dois ambientes, o que evita um caminho de código que só roda em teste.
//
// As credenciais vêm da cadeia padrão do SDK — variáveis de ambiente, perfil,
// metadados da instância. Nenhuma credencial é lida ou guardada por este
// código: o controle de acesso à fila fica com as políticas do broker.
func NewClient(ctx context.Context, cfg config.SQS) (*sqs.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	aws, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("sqs: falha ao carregar a configuração AWS: %w", err)
	}

	return sqs.NewFromConfig(aws, func(o *sqs.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
		}
	}), nil
}
