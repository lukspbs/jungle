// Package dbtest fornece um PostgreSQL real para os testes de integração.
//
// É o ponto único de obtenção do banco nos testes. Hoje ele consome
// TEST_DATABASE_URL; trocar a origem por containers gerenciados pelo próprio
// teste não exige tocar em nenhum caso de teste, porque todos passam por aqui.
//
// Os testes não limpam tabelas entre execuções: cada um usa identificadores
// próprios e consulta apenas os seus. Isso não é conveniência, é consequência
// do desenho — o ledger recusa DELETE e TRUNCATE, então uma suíte que
// dependesse de limpeza estaria brigando com a garantia que ela deveria estar
// verificando.
package dbtest

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// EnvURL é a variável que aponta para o banco de testes.
const EnvURL = "TEST_DATABASE_URL"

var (
	once       sync.Once
	migrateErr error
)

// Store devolve um Store com o schema aplicado.
//
// Pula o teste quando TEST_DATABASE_URL não está definida, de modo que
// `go test ./...` continua verde numa máquina sem banco. Os comandos que
// exigem a infraestrutura estão documentados no README.
func Store(t *testing.T) *postgres.Store {
	t.Helper()
	return postgres.NewStore(Pool(t))
}

// Pool devolve um pool conectado ao banco de testes, com as migrations já
// aplicadas.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s não definida: teste de integração pulado", EnvURL)
	}

	// As migrations são aplicadas uma vez por processo de teste. O advisory
	// lock do migrator cobre a concorrência entre pacotes de teste.
	once.Do(func() { migrateErr = applyMigrations(url) })
	if migrateErr != nil {
		t.Fatalf("falha ao preparar o schema de teste: %v", migrateErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, config.Database{
		URL:              url,
		MaxConns:         10,
		MinConns:         1,
		MaxConnLifetime:  time.Hour,
		ConnectTimeout:   5 * time.Second,
		StatementTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("falha ao conectar no banco de teste: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// DrainOutbox marca como publicados todos os eventos pendentes no momento da
// chamada.
//
// Os testes que medem a fila — reivindicação concorrente, lease, reagendamento
// — afirmam coisas sobre quem está na frente. Como a suíte compartilha o banco
// e eventos de execuções anteriores continuam pendentes para sempre, sem drenar
// eles seriam reivindicados primeiro e os eventos do teste nunca chegariam a
// ser atendidos. Drenar é legítimo: a outbox é uma fila, e marcar publicado é
// a operação normal dela.
//
// A drenagem é global, e não tem como não ser: ela existe justamente para
// limpar o que outros deixaram. Isso faz dela incompatível com pacotes rodando
// em paralelo — o Go roda pacotes concorrentemente por padrão, e uma drenagem
// no pacote postgres marcaria publicado o evento que um teste do pacote app
// acabou de gravar e ainda não publicou. Por isso a suíte com infraestrutura
// roda com `-p 1`, e o README diz isso junto do comando.
func DrainOutbox(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = now(), locked_by = NULL, locked_until = NULL
		 WHERE published_at IS NULL`); err != nil {
		t.Fatalf("falha ao drenar a outbox: %v", err)
	}
}

func applyMigrations(url string) error {
	migrator, err := postgres.NewMigrator(url)
	if err != nil {
		return err
	}
	defer migrator.Close()
	return migrator.Up()
}
