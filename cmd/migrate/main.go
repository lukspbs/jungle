// Command migrate aplica e reverte o schema.
//
// Existe como binário separado da aplicação de propósito. Aplicar migrations
// no start-up de cada instância acopla o ciclo de vida do schema ao do
// processo; separado, o Docker Compose roda este comando uma vez antes de
// subir as instâncias, e o operador reverte sem precisar derrubar a aplicação.
//
//	migrate up          aplica todas as migrations pendentes
//	migrate down        reverte todas as migrations
//	migrate steps N     aplica N migrations (ou reverte, se N for negativo)
//	migrate version     mostra a versão aplicada
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/platform/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("comando obrigatório: up, down, steps ou version")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	migrator, err := postgres.NewMigrator(cfg.Database.URL)
	if err != nil {
		return err
	}
	defer migrator.Close()

	switch args[0] {
	case "up":
		if err := migrator.Up(); err != nil {
			return err
		}
		return report(migrator, "migrations aplicadas")

	case "down":
		if err := migrator.Down(); err != nil {
			return err
		}
		return report(migrator, "migrations revertidas")

	case "steps":
		if len(args) < 2 {
			return errors.New("steps exige o número de passos, por exemplo: steps -1")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("steps: %q não é inteiro", args[1])
		}
		if err := migrator.Steps(n); err != nil {
			return err
		}
		return report(migrator, fmt.Sprintf("%d passo(s) aplicado(s)", n))

	case "version":
		return report(migrator, "versão atual")

	default:
		return fmt.Errorf("comando desconhecido: %q", args[0])
	}
}

// report imprime a versão resultante. Schema sujo é erro: significa que uma
// migration falhou no meio e o estado precisa de intervenção antes de seguir.
func report(migrator *postgres.Migrator, prefix string) error {
	version, dirty, err := migrator.Version()
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("schema sujo na versão %d: uma migration falhou no meio e exige intervenção", version)
	}
	fmt.Printf("%s — versão %d\n", prefix, version)
	return nil
}
