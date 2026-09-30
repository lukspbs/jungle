package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

// LedgerRepository persiste os lançamentos do ledger.
//
// Só há inserção e leitura: alterar ou apagar é recusado pelas triggers e pelos
// privilégios do banco, então nem existe método para tentar.
type LedgerRepository struct {
	db DBTX
}

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
	balance_before_minor, balance_after_minor, created_at`

// LedgerPage é uma página do ledger com o cursor da próxima.
type LedgerPage struct {
	Entries []*wallet.LedgerEntry

	// NextCursor é a sequência a partir da qual continuar, ou zero quando a
	// página atual esgotou o ledger. A codificação opaca do cursor é
	// responsabilidade da borda HTTP; aqui ele é a sequência interna.
	NextCursor int64
}

// LedgerSummary é o resultado da reconstrução do saldo a partir do ledger.
type LedgerSummary struct {
	Balance money.Money
	Entries int
}

// Insert grava um lançamento.
func (r *LedgerRepository) Insert(ctx context.Context, e *wallet.LedgerEntry) error {
	const query = `
		INSERT INTO wallet_ledger_entries (` + ledgerColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := r.db.Exec(ctx, query,
		e.ID(), e.WalletID(), e.TransactionID(), e.Direction().String(),
		e.Amount().MinorUnits(), e.Amount().Currency().String(),
		e.BalanceBefore().MinorUnits(), e.BalanceAfter().MinorUnits(),
		e.CreatedAt())
	if err != nil {
		return fmt.Errorf("postgres: falha ao inserir lançamento: %w", classify(err))
	}
	return nil
}

// ListByWallet devolve uma página do ledger em ordem estável.
//
// A ordenação é pela sequência interna e não por created_at: lançamentos do
// mesmo commit compartilham o instante, e um cursor sobre coluna com empate
// pula ou repete linhas entre páginas.
func (r *LedgerRepository) ListByWallet(
	ctx context.Context, walletID uuid.UUID, afterSeq int64, limit int,
) (LedgerPage, error) {
	const query = `
		SELECT ` + ledgerColumns + `, seq
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3`

	// Busca um a mais para saber se existe próxima página sem uma segunda
	// consulta de contagem.
	rows, err := r.db.Query(ctx, query, walletID, afterSeq, limit+1)
	if err != nil {
		return LedgerPage{}, fmt.Errorf("postgres: falha ao listar o ledger: %w", classify(err))
	}
	defer rows.Close()

	var (
		page LedgerPage
		seqs []int64
	)
	for rows.Next() {
		var (
			snapshot  wallet.LedgerEntrySnapshot
			direction string
			currency  string
			amount    int64
			before    int64
			after     int64
			seq       int64
		)
		if err := rows.Scan(
			&snapshot.ID, &snapshot.WalletID, &snapshot.TransactionID, &direction,
			&amount, &currency, &before, &after, &snapshot.CreatedAt, &seq,
		); err != nil {
			return LedgerPage{}, fmt.Errorf("postgres: falha ao ler lançamento: %w", err)
		}

		if snapshot.Direction, err = wallet.ParseDirection(direction); err != nil {
			return LedgerPage{}, err
		}
		if snapshot.Amount, err = toMoney(amount, currency); err != nil {
			return LedgerPage{}, err
		}
		if snapshot.BalanceBefore, err = toMoney(before, currency); err != nil {
			return LedgerPage{}, err
		}
		if snapshot.BalanceAfter, err = toMoney(after, currency); err != nil {
			return LedgerPage{}, err
		}

		entry, err := wallet.RehydrateLedgerEntry(snapshot)
		if err != nil {
			return LedgerPage{}, err
		}
		page.Entries = append(page.Entries, entry)
		seqs = append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		return LedgerPage{}, fmt.Errorf("postgres: falha ao percorrer o ledger: %w", err)
	}

	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.NextCursor = seqs[limit-1]
	}
	return page, nil
}

// Summarize reconstrói o saldo somando créditos e subtraindo débitos.
//
// A soma acontece no banco, em BIGINT, e não em memória: trazer o ledger
// inteiro para somar em Go seria O(n) em rede e não caberia em carteiras
// longas. A aritmética continua inteira de ponta a ponta.
func (r *LedgerRepository) Summarize(
	ctx context.Context, walletID uuid.UUID, currency money.Currency,
) (LedgerSummary, error) {
	const query = `
		SELECT COALESCE(SUM(
		           CASE direction
		               WHEN 'CREDIT' THEN amount_minor
		               ELSE -amount_minor
		           END), 0)::BIGINT AS balance_minor,
		       COUNT(*) AS entries
		  FROM wallet_ledger_entries
		 WHERE wallet_id = $1`

	var (
		balanceMinor int64
		entries      int
	)
	if err := r.db.QueryRow(ctx, query, walletID).Scan(&balanceMinor, &entries); err != nil {
		return LedgerSummary{}, fmt.Errorf("postgres: falha ao reconstruir o saldo: %w", classify(err))
	}

	balance, err := money.FromMinorUnits(balanceMinor, currency)
	if err != nil {
		return LedgerSummary{}, err
	}
	return LedgerSummary{Balance: balance, Entries: entries}, nil
}
