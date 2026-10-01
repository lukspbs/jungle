package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX é o subconjunto do pgx satisfeito tanto pelo pool quanto por uma
// transação. Os repositórios dependem dele, e não do tipo concreto, de modo
// que a mesma implementação serve dentro e fora de transação.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store é a porta de entrada da persistência e o dono da delimitação
// transacional.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore monta o store sobre o pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Repositories agrupa os repositórios que compartilham um mesmo executor.
//
// Agrupar é o que dá sentido à delimitação: dentro de InTx todos apontam para
// a mesma transação, então o estado da operação, o saldo, o lançamento, a
// inbox e a outbox são confirmados atomicamente. Não existe caminho para um
// repositório escapar da transação por engano, porque nenhum deles guarda o
// pool.
type Repositories struct {
	Wallets      *WalletRepository
	Transactions *TransactionRepository
	Ledger       *LedgerRepository
	Outbox       *OutboxRepository
}

func newRepositories(db DBTX) *Repositories {
	return &Repositories{
		Wallets:      &WalletRepository{db: db},
		Transactions: &TransactionRepository{db: db},
		Ledger:       &LedgerRepository{db: db},
		Outbox:       &OutboxRepository{db: db},
	}
}

// Read devolve repositórios fora de transação, para consultas que não
// participam de nenhuma escrita.
func (s *Store) Read() *Repositories { return newRepositories(s.pool) }

// InTx executa fn dentro de uma transação SQL.
//
// O nível é READ COMMITTED, o padrão do PostgreSQL. A coordenação entre
// escritores da mesma carteira vem do SELECT ... FOR UPDATE explícito em
// WalletRepository.LockForUpdate, não do nível de isolamento: um lock de linha
// dá exclusão mútua determinística por carteira, sem as falhas de serialização
// que REPEATABLE READ produziria sob as 50 duplicatas simultâneas do teste
// obrigatório. Carteiras distintas travam linhas distintas e seguem em
// paralelo — não há lock global em lugar nenhum.
//
// Um erro devolvido por fn aborta a transação. Um panic também: o rollback
// está em defer e o panic é repropagado depois dele.
func (s *Store) InTx(ctx context.Context, fn func(context.Context, *Repositories) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("postgres: falha ao iniciar transação: %w", err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		// O contexto de fn pode já estar cancelado; o rollback precisa de um
		// contexto próprio para não virar conexão abandonada.
		rollbackCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if err := fn(ctx, newRepositories(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: falha ao confirmar transação: %w", classify(err))
	}
	committed = true
	return nil
}

// Ping confirma que o banco responde. Usado pelo health check de readiness.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: banco indisponível: %w", err)
	}
	return nil
}

// Close fecha o pool.
func (s *Store) Close() { s.pool.Close() }

// noRows informa se o erro é a ausência de linha, que os repositórios traduzem
// para o seu próprio erro de "não encontrado".
func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
