package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/domain/wallet"
	"github.com/lukspbs/jungle/internal/platform/logging"
	"github.com/lukspbs/jungle/internal/platform/metrics"
)

// Queries reúne as consultas de leitura.
//
// Elas ficam separadas dos casos de uso de escrita porque não abrem transação:
// uma leitura que não movimenta nada não precisa de lock nem de commit, e
// misturá-las obrigaria o leitor do código a conferir, caso a caso, qual é qual.
type Queries struct {
	store   *postgres.Store
	metrics *metrics.Metrics
}

// NewQueries monta as consultas.
func NewQueries(store *postgres.Store, m *metrics.Metrics) *Queries {
	if m == nil {
		m = metrics.New()
	}
	return &Queries{store: store, metrics: m}
}

// Wallet devolve uma carteira.
func (q *Queries) Wallet(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	w, err := q.store.Read().Wallets.FindByID(ctx, id)
	if errors.Is(err, postgres.ErrWalletNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrWalletNotFound, id)
	}
	return w, err
}

// LedgerPage é uma página do extrato.
type LedgerPage struct {
	Entries    []*wallet.LedgerEntry
	NextCursor int64
}

// Ledger devolve uma página do extrato de uma carteira.
//
// A existência da carteira é confirmada antes: sem isso, um id inexistente
// devolveria página vazia e o cliente não saberia distinguir "carteira sem
// movimentação" de "carteira que não existe".
func (q *Queries) Ledger(
	ctx context.Context, walletID uuid.UUID, afterSeq int64, limit int,
) (LedgerPage, error) {
	if _, err := q.Wallet(ctx, walletID); err != nil {
		return LedgerPage{}, err
	}
	pagina, err := q.store.Read().Ledger.ListByWallet(ctx, walletID, afterSeq, limit)
	if err != nil {
		return LedgerPage{}, err
	}
	return LedgerPage{Entries: pagina.Entries, NextCursor: pagina.NextCursor}, nil
}

// Transaction devolve uma transação pelo identificador interno.
func (q *Queries) Transaction(
	ctx context.Context, id uuid.UUID,
) (*wagering.WagerTransaction, error) {
	tx, err := q.store.Read().Transactions.FindByID(ctx, id)
	if errors.Is(err, postgres.ErrTransactionNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrTransactionNotFound, id)
	}
	return tx, err
}

// ProviderTransaction devolve a transação de um provedor pelo id externo.
//
// A busca é sempre escopada pelo provedor. É isso que sustenta o isolamento:
// não existe caminho que resolva um id externo sem dizer de quem ele é.
func (q *Queries) ProviderTransaction(
	ctx context.Context, providerID, externalTransactionID string,
) (*wagering.WagerTransaction, error) {
	tx, err := q.store.Read().Transactions.FindByProviderAndExternalID(
		ctx, providerID, externalTransactionID)
	if errors.Is(err, postgres.ErrTransactionNotFound) {
		return nil, fmt.Errorf("%w: %s/%s", ErrTransactionNotFound, providerID, externalTransactionID)
	}
	return tx, err
}

// Reconciliation compara o saldo armazenado com o reconstruído pelo ledger.
type Reconciliation struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int
}

// Reconcile reconstrói o saldo a partir do ledger e compara com o armazenado.
//
// A leitura acontece em InSnapshot, e não em InTx, porque a comparação só faz
// sentido sobre uma visão única: o saldo e o ledger são lidos por comandos
// diferentes, e em READ COMMITTED cada comando pegaria um snapshot novo. Uma
// aposta confirmada entre as duas leituras apareceria como divergência — alarme
// falso justamente na métrica que existe para denunciar divergência de verdade.
//
// A reconciliação não altera o saldo. Ela relata.
func (q *Queries) Reconcile(ctx context.Context, walletID uuid.UUID) (Reconciliation, error) {
	var resultado Reconciliation

	err := q.store.InSnapshot(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		w, err := r.Wallets.FindByID(ctx, walletID)
		if errors.Is(err, postgres.ErrWalletNotFound) {
			return fmt.Errorf("%w: %s", ErrWalletNotFound, walletID)
		}
		if err != nil {
			return err
		}

		resumo, err := r.Ledger.Summarize(ctx, walletID, w.Currency())
		if err != nil {
			return err
		}

		// difference é o armazenado menos o reconstruído, conforme o contrato.
		diferenca, err := w.Balance().Sub(resumo.Balance)
		if err != nil {
			return err
		}

		resultado = Reconciliation{
			WalletID:          walletID,
			StoredBalance:     w.Balance(),
			CalculatedBalance: resumo.Balance,
			Difference:        diferenca,
			Consistent:        diferenca.IsZero(),
			CheckedEntries:    resumo.Entries,
		}
		return nil
	})
	if err == nil {
		// A divergência é reportada na resposta, no log e aqui. Um painel que
		// mostre esta série diferente de zero é o sinal mais barato de que algo
		// saiu do lugar.
		q.metrics.ObserveReconciliation(resultado.Consistent)
		if !resultado.Consistent {
			logging.From(ctx).ErrorContext(ctx, "divergência na reconciliação",
				slog.String(logging.FieldWalletID, walletID.String()),
				slog.String("storedBalance", resultado.StoredBalance.String()),
				slog.String("calculatedBalance", resultado.CalculatedBalance.String()),
				slog.String("difference", resultado.Difference.String()),
				slog.Int("checkedEntries", resultado.CheckedEntries))
		}
	}
	return resultado, err
}

// Readiness confere se as dependências externas respondem.
//
// É o que o health check de readiness consulta. Fica aqui, e não na borda HTTP,
// porque a lista de dependências é da aplicação: a borda só sabe traduzir o
// resultado em código de resposta.
type Readiness struct {
	store *postgres.Store
}

// NewReadiness monta o verificador.
func NewReadiness(store *postgres.Store) *Readiness { return &Readiness{store: store} }

// Check consulta cada dependência.
func (r *Readiness) Check(ctx context.Context) error {
	if err := r.store.Ping(ctx); err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	return nil
}
