package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

// WalletRepository persiste o agregado da carteira.
type WalletRepository struct {
	db DBTX
}

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

// Insert grava uma carteira recém-aberta.
func (r *WalletRepository) Insert(ctx context.Context, w *wallet.Wallet) error {
	const query = `
		INSERT INTO wallets (` + walletColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.db.Exec(ctx, query,
		w.ID(), w.PlayerID(), w.Currency().String(),
		w.Balance().MinorUnits(), w.Version(),
		w.CreatedAt(), w.UpdatedAt())
	if err != nil {
		return fmt.Errorf("postgres: falha ao inserir carteira: %w", classify(err))
	}
	return nil
}

// FindByID lê uma carteira sem travá-la. Serve para consultas; uma
// movimentação precisa de LockForUpdate.
func (r *WalletRepository) FindByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	const query = `SELECT ` + walletColumns + ` FROM wallets WHERE id = $1`
	return r.scanOne(ctx, query, id)
}

// FindByPlayerAndCurrency lê a carteira do par (jogador, moeda).
func (r *WalletRepository) FindByPlayerAndCurrency(
	ctx context.Context, playerID uuid.UUID, currency money.Currency,
) (*wallet.Wallet, error) {
	const query = `SELECT ` + walletColumns + ` FROM wallets WHERE player_id = $1 AND currency = $2`
	return r.scanOne(ctx, query, playerID, currency.String())
}

// LockForUpdate lê a carteira segurando um lock exclusivo de linha até o fim
// da transação.
//
// Este é o ponto de coordenação entre escritores. O lock é por linha, então
// duas apostas na mesma carteira serializam aqui, enquanto carteiras distintas
// travam linhas distintas e seguem em paralelo. É o que satisfaz a exigência
// de coordenação por carteira sem nenhum lock global.
//
// Só faz sentido dentro de Store.InTx: fora de transação o lock seria liberado
// imediatamente.
func (r *WalletRepository) LockForUpdate(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	const query = `SELECT ` + walletColumns + ` FROM wallets WHERE id = $1 FOR UPDATE`
	return r.scanOne(ctx, query, id)
}

// UpdateBalance grava o novo saldo, exigindo que a versão esperada ainda esteja
// lá.
//
// Com o lock pessimista em LockForUpdate a disputa já foi resolvida, e a
// cláusula de versão nunca deveria falhar. Ela existe como detecção: se falhar,
// alguém escreveu por um caminho que não passou pelo lock, e é melhor abortar
// ruidosamente do que sobrescrever uma atualização confirmada. A trigger do
// banco impõe, em paralelo, que a versão avance exatamente um passo.
func (r *WalletRepository) UpdateBalance(
	ctx context.Context, w *wallet.Wallet, expectedVersion int64,
) error {
	const query = `
		UPDATE wallets
		   SET balance_minor = $1, version = $2, updated_at = $3
		 WHERE id = $4 AND version = $5`

	tag, err := r.db.Exec(ctx, query,
		w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), w.ID(), expectedVersion)
	if err != nil {
		return fmt.Errorf("postgres: falha ao atualizar saldo: %w", classify(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: carteira %s não estava mais na versão %d",
			ErrConcurrentUpdate, w.ID(), expectedVersion)
	}
	return nil
}

func (r *WalletRepository) scanOne(ctx context.Context, query string, args ...any) (*wallet.Wallet, error) {
	var (
		snapshot     wallet.Snapshot
		currencyCode string
		balanceMinor int64
	)

	err := r.db.QueryRow(ctx, query, args...).Scan(
		&snapshot.ID, &snapshot.PlayerID, &currencyCode, &balanceMinor,
		&snapshot.Version, &snapshot.CreatedAt, &snapshot.UpdatedAt)
	if noRows(err) {
		return nil, ErrWalletNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao ler carteira: %w", classify(err))
	}

	if snapshot.Balance, err = toMoney(balanceMinor, currencyCode); err != nil {
		return nil, err
	}
	return wallet.Rehydrate(snapshot)
}
