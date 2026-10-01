package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/lukspbs/jungle/internal/platform/config"
)

// Module provê o pool e o store.
//
// O pool entra no ciclo de vida com um hook de parada. O Fx encerra na ordem
// inversa da inicialização, e o pool é a primeira dependência a subir — logo é
// a última a cair, depois de servidor e workers já terem parado de usá-lo.
var Module = fx.Module("postgres",
	fx.Provide(
		newPoolWithLifecycle,
		NewStore,
	),
)

func newPoolWithLifecycle(
	lc fx.Lifecycle, cfg config.Database,
) (*pgxpool.Pool, error) {
	// A conexão é aberta na construção, e não no hook de start, porque uma
	// dependência indisponível precisa impedir a aplicação de subir — e não
	// deixá-la subir para falhar na primeira aposta.
	pool, err := NewPool(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}
