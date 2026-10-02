// Command api é o serviço de processamento de apostas.
//
// A composição é feita por Uber Fx: cada pacote exporta o seu módulo, e aqui
// eles são apenas reunidos. A ordem importa para o encerramento — o Fx desfaz
// na ordem inversa, então o banco, declarado primeiro, é fechado por último,
// depois de servidor e workers terem parado de usá-lo.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	adapterhttp "github.com/lukspbs/jungle/internal/adapter/http"
	"github.com/lukspbs/jungle/internal/adapter/postgres"
	adaptersqs "github.com/lukspbs/jungle/internal/adapter/sqs"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/platform/config"
	"github.com/lukspbs/jungle/internal/platform/logging"
	"github.com/lukspbs/jungle/internal/platform/metrics"
	"github.com/lukspbs/jungle/internal/platform/worker"
)

func main() {
	// A imagem final é distroless: não há shell nem curl para um healthcheck
	// do Docker. O próprio binário faz a sondagem quando chamado com --health,
	// o que evita acrescentar um utilitário só para isso.
	if len(os.Args) > 1 && os.Args[1] == "--health" {
		os.Exit(probe())
	}
	fx.New(Modules()).Run()
}

// probe consulta o readiness local e devolve o código de saída.
func probe() int {
	porta := os.Getenv("HTTP_PORT")
	if porta == "" {
		porta = "8080"
	}

	cliente := &http.Client{Timeout: 3 * time.Second}
	res, err := cliente.Get("http://127.0.0.1:" + porta + "/health/ready")
	if err != nil {
		fmt.Fprintf(os.Stderr, "health: %v\n", err)
		return 1
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health: readiness devolveu %d\n", res.StatusCode)
		return 1
	}
	return 0
}

// Modules reúne a composição.
//
// Exportada para que o teste de composição monte a mesma aplicação que o
// binário monta, em vez de uma versão aproximada que poderia divergir.
func Modules() fx.Option {
	return fx.Options(
		config.Module,
		logging.Module,
		metrics.Module,
		postgres.Module,
		app.Module,
		adaptersqs.Module,
		adapterhttp.Module,
		worker.Module,

		// O prazo cobre o encerramento inteiro: drenar as requisições em
		// andamento, confirmar o término dos workers e fechar o pool.
		fx.StartTimeout(30*time.Second),
		fx.StopTimeout(45*time.Second),

		fx.WithLogger(fxLogger),
	)
}

// fxLogger manda os eventos de ciclo de vida do Fx para o mesmo logger
// estruturado da aplicação.
//
// Eles descrevem a montagem do grafo e o encerramento — exatamente o que
// interessa quando uma instância não sobe ou demora a encerrar — e em JSON
// ficam consultáveis junto com o resto.
func fxLogger(logger *slog.Logger) fxevent.Logger {
	return &fxevent.SlogLogger{Logger: logger.With(slog.String("component", "fx"))}
}
