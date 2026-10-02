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
// limpar o que outros deixaram. Isso a tornava incompatível com pacotes
// rodando em paralelo — o Go roda pacotes concorrentemente por padrão, e uma
// drenagem no pacote postgres marcava publicado o evento que um teste do
// pacote app acabou de gravar e ainda não publicou. Por isso quem drena
// primeiro toma a trava descrita em travarOutbox.
func DrainOutbox(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	travarOutbox(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = now(), locked_by = NULL, locked_until = NULL
		 WHERE published_at IS NULL`); err != nil {
		t.Fatalf("falha ao drenar a outbox: %v", err)
	}
}

// chaveTravaOutbox identifica o advisory lock que serializa os testes de
// outbox. O valor é arbitrário — só precisa não colidir com o do migrator.
const chaveTravaOutbox int64 = 0x6A756E676C65 // "jungle" em ASCII

var (
	muTravadas sync.Mutex
	travadas   = map[*testing.T]struct{}{}
)

// travarOutbox serializa, entre processos, os testes que medem a outbox.
//
// A trava é do banco e não do processo porque o problema também é: `go test
// ./...` roda cada pacote num processo próprio e vários ao mesmo tempo, então
// um mutex em Go não alcançaria o pacote vizinho. Um advisory lock alcança,
// porque os dois processos falam com o mesmo PostgreSQL.
//
// Ela vale do primeiro DrainOutbox até o fim do teste, e não só durante a
// drenagem: o que precisa de exclusividade é a janela inteira entre drenar e
// conferir, já que é nela que uma drenagem alheia faria estrago.
//
// O laço usa pg_try_advisory_lock em vez de pg_advisory_lock, que bloquearia:
// o pool impõe statement_timeout, e uma espera longa dentro do próprio comando
// seria abortada por ele. Tentar e dormir mantém cada comando instantâneo.
func travarOutbox(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	// Um mesmo teste pode drenar mais de uma vez; a trava é tomada só na
	// primeira, senão o unlock do cleanup não casaria com os locks tomados.
	muTravadas.Lock()
	_, repetida := travadas[t]
	travadas[t] = struct{}{}
	muTravadas.Unlock()
	if repetida {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conexao, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("falha ao obter conexão para a trava da outbox: %v", err)
	}

	for {
		var obtida bool
		if err := conexao.QueryRow(ctx,
			"SELECT pg_try_advisory_lock($1)", chaveTravaOutbox).Scan(&obtida); err != nil {
			conexao.Release()
			t.Fatalf("falha ao tentar travar a outbox: %v", err)
		}
		if obtida {
			break
		}
		select {
		case <-ctx.Done():
			conexao.Release()
			t.Fatal("a trava da outbox não foi liberada no prazo")
		case <-time.After(50 * time.Millisecond):
		}
	}

	t.Cleanup(func() {
		liberacao, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelar()

		// O unlock é explícito porque a conexão volta para o pool em vez de
		// fechar: sem ele a trava sobreviveria ao teste, presa numa sessão que
		// segue viva.
		if _, err := conexao.Exec(liberacao,
			"SELECT pg_advisory_unlock($1)", chaveTravaOutbox); err != nil {
			t.Errorf("falha ao liberar a trava da outbox: %v", err)
		}
		conexao.Release()

		muTravadas.Lock()
		delete(travadas, t)
		muTravadas.Unlock()
	})
}

func applyMigrations(url string) error {
	migrator, err := postgres.NewMigrator(url)
	if err != nil {
		return err
	}
	defer migrator.Close()
	return migrator.Up()
}
