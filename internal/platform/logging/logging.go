// Package logging configura o log estruturado do serviço.
//
// O formato é JSON porque o destino é um coletor, não um terminal: campos
// nomeados são consultáveis, texto corrido não.
//
// # O que nunca é registrado
//
// Token, segredo de cliente, cabeçalho Authorization e corpo de requisição.
// Um payload financeiro completo também não: os identificadores bastam para
// rastrear a operação, e registrar valores em log os espalha por sistemas que
// não têm o mesmo controle de acesso do banco.
//
// O que é registrado são os identificadores da operação — correlationId,
// messageId, transactionId, walletId, providerId — e o desfecho.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/lukspbs/jungle/internal/platform/config"
)

type contextKey struct{}

// New monta o logger raiz.
//
// Toda linha carrega o ambiente e o identificador da instância: num cluster,
// saber de qual processo veio a linha é o que permite correlacionar um lease da
// outbox com quem o tomou.
func New(cfg config.App, saida io.Writer) *slog.Logger {
	if saida == nil {
		saida = os.Stdout
	}

	handler := slog.NewJSONHandler(saida, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// O instante sai em RFC 3339 com milissegundos e em UTC, igual aos
			// eventos de integração. Dois formatos de tempo no mesmo sistema
			// atrapalham a correlação.
			if a.Key == slog.TimeKey {
				return slog.String("ts", a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
			}
			return a
		},
	})

	return slog.New(handler).With(
		slog.String("env", cfg.Environment),
		slog.String("instance", cfg.InstanceID),
	)
}

func parseLevel(nivel string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(nivel)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Into guarda o logger no contexto.
func Into(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// From devolve o logger do contexto.
//
// Sem logger no contexto devolve o padrão, nunca nil: um caminho que esquece de
// propagar o contexto perde os campos, mas não derruba o processo.
func From(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// With deriva um contexto cujo logger carrega os campos adicionais.
//
// É assim que o identificador da operação desce pela pilha: a borda HTTP
// acrescenta o correlationId, o consumidor acrescenta o messageId, o caso de
// uso acrescenta o transactionId. Cada camada só sabe o que ela própria
// descobriu.
func With(ctx context.Context, args ...any) context.Context {
	return Into(ctx, From(ctx).With(args...))
}

// Campos padronizados. Constantes evitam que a mesma informação apareça como
// "wallet_id" numa linha e "walletId" em outra, o que quebraria uma busca.
const (
	FieldCorrelationID = "correlationId"
	FieldMessageID     = "messageId"
	FieldTransactionID = "transactionId"
	FieldWalletID      = "walletId"
	FieldProviderID    = "providerId"
	FieldExternalID    = "externalTransactionId"
	FieldEventID       = "eventId"
	FieldStatus        = "status"
	FieldFailureCode   = "failureCode"
	FieldKind          = "kind"
	FieldDurationMs    = "durationMs"
)
