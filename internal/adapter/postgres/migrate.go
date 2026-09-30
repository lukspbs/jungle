package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// Registra o driver "pgx5" usado na URL do migrator.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/lukspbs/jungle/migrations"
)

// Migrator aplica e reverte as migrations embutidas.
//
// O golang-migrate toma um advisory lock no PostgreSQL durante a aplicação, de
// modo que várias instâncias subindo ao mesmo tempo não disputam o schema: uma
// aplica e as outras esperam. Esse lock é de bootstrap e não tem relação com a
// coordenação por carteira, que nunca usa lock global.
type Migrator struct {
	m *migrate.Migrate
}

// NewMigrator monta o migrator sobre as migrations embutidas.
func NewMigrator(databaseURL string) (*Migrator, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao ler migrations embutidas: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, migrateURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao montar o migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Up aplica todas as migrations pendentes. Nenhuma pendência não é erro.
func (mig *Migrator) Up() error {
	if err := mig.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: falha ao aplicar migrations: %w", err)
	}
	return nil
}

// Down reverte todas as migrations. Destrutivo: existe para o ciclo de
// desenvolvimento e para provar que a reversão funciona.
func (mig *Migrator) Down() error {
	if err := mig.m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: falha ao reverter migrations: %w", err)
	}
	return nil
}

// Steps aplica (n positivo) ou reverte (n negativo) um número de migrations.
func (mig *Migrator) Steps(n int) error {
	if err := mig.m.Steps(n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: falha ao mover %d migrations: %w", n, err)
	}
	return nil
}

// Version devolve a versão aplicada e se o schema está sujo. Sujo significa
// que uma migration falhou no meio e exige intervenção antes de prosseguir.
func (mig *Migrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = mig.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("postgres: falha ao ler a versão do schema: %w", err)
	}
	return version, dirty, nil
}

// Close libera os recursos do migrator.
func (mig *Migrator) Close() error {
	sourceErr, dbErr := mig.m.Close()
	return errors.Join(sourceErr, dbErr)
}

// migrateURL troca o esquema da connection string pelo driver registrado.
// A mesma DATABASE_URL serve para a aplicação e para as migrations, evitando
// duas variáveis que podem divergir.
func migrateURL(databaseURL string) string {
	for _, scheme := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(databaseURL, scheme) {
			return "pgx5://" + strings.TrimPrefix(databaseURL, scheme)
		}
	}
	return databaseURL
}
