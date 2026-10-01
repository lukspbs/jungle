package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"go.uber.org/fx"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/platform/auth"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// Module provê e inicia o servidor HTTP.
var Module = fx.Module("http",
	fx.Provide(
		func(r *app.Readiness) ReadinessChecker { return r },
		newVerifier,
		NewHandlers,
		NewRouter,
		newServer,
	),
	// O Invoke é o que faz o servidor existir de fato: sem alguém que dependa
	// dele, o Fx não construiria nada.
	fx.Invoke(func(*http.Server) {}),
)

// newVerifier descobre o emissor na construção: um IdP inacessível impede a
// aplicação de subir, em vez de deixá-la aceitar requisições que não consegue
// autenticar.
func newVerifier(cfg config.Auth) (*auth.Verifier, error) {
	return auth.NewVerifier(context.Background(), cfg)
}

func newServer(lc fx.Lifecycle, cfg config.HTTP, handler http.Handler) *http.Server {
	servidor := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// A porta é aberta aqui, dentro do start, e não na goroutine: se
			// ela estiver ocupada, a aplicação precisa falhar ao subir em vez
			// de subir e ficar sem atender.
			listener, err := net.Listen("tcp", servidor.Addr)
			if err != nil {
				return fmt.Errorf("http: falha ao escutar em %s: %w", servidor.Addr, err)
			}
			go func() {
				if err := servidor.Serve(listener); err != nil &&
					!errors.Is(err, http.ErrServerClosed) {
					// Serve só retorna erro diferente de ErrServerClosed quando
					// o listener morre; o processo já está encerrando.
					_ = err
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// Shutdown para de aceitar conexões novas e espera as requisições
			// em andamento terminarem dentro do prazo do contexto, que o Fx
			// deriva do StopTimeout.
			return servidor.Shutdown(ctx)
		},
	})

	return servidor
}
