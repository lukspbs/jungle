// Command api é o serviço de processamento de apostas.
//
// A composição é feita por Uber Fx: cada pacote exporta o seu módulo, e aqui
// eles são apenas reunidos. A ordem importa para o encerramento — o Fx desfaz
// na ordem inversa, então o banco, declarado primeiro, é fechado por último,
// depois de servidor e workers terem parado de usá-lo.
package main

import (
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	adapterhttp "github.com/lukspbs/jungle/internal/adapter/http"
	"github.com/lukspbs/jungle/internal/adapter/postgres"
	adaptersqs "github.com/lukspbs/jungle/internal/adapter/sqs"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/platform/config"
	"github.com/lukspbs/jungle/internal/platform/worker"
)

func main() {
	fx.New(Modules()).Run()
}

// Modules reúne a composição.
//
// Exportada para que o teste de composição monte a mesma aplicação que o
// binário monta, em vez de uma versão aproximada que poderia divergir.
func Modules() fx.Option {
	return fx.Options(
		config.Module,
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

// fxLogger mantém o log de ciclo de vida do Fx em stderr, separado do log
// estruturado da aplicação. Os eventos do Fx descrevem a montagem do grafo e o
// encerramento, que interessam ao operador e não ao consumidor da API.
func fxLogger() fxevent.Logger {
	return &fxevent.ConsoleLogger{W: os.Stderr}
}
