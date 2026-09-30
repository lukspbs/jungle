// Package postgres implementa a persistência em PostgreSQL com pgx e SQL
// explícito.
//
// O SQL fica visível no código, sem geração nem ORM, porque as garantias do
// desafio dependem de detalhes que precisam ser auditáveis: onde está o
// FOR UPDATE, qual constraint produz o conflito, e o que está dentro de qual
// transação.
package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lukspbs/jungle/internal/platform/config"
)

// NewPool abre o pool de conexões e confirma que o banco responde.
//
// Cada instância tem o seu próprio pool: o desafio exige que as garantias
// valham entre processos independentes, então nada de estado compartilhado
// entre instâncias fora do banco.
func NewPool(ctx context.Context, cfg config.Database) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: DATABASE_URL inválida: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// statement_timeout no servidor limita a duração de um comando. Sem ele,
	// um cliente travado seguraria o lock de uma carteira indefinidamente e
	// bloquearia todas as operações daquele jogador.
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] =
		strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "jungle-wagering"

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao abrir o pool: %w", err)
	}

	// Falhar aqui é melhor que falhar na primeira aposta: a validação de
	// dependências acontece na inicialização.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: banco não respondeu ao ping: %w", err)
	}

	return pool, nil
}
