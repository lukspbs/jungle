package http

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wallet"
)

// Os identificadores chegam como string e não como uuid.UUID para que um valor
// malformado produza uma resposta 400 com mensagem útil, em vez do erro
// genérico de desserialização.

type openWalletRequest struct {
	PlayerID       string      `json:"playerId"`
	InitialBalance money.Money `json:"initialBalance"`
}

type walletResponse struct {
	ID       uuid.UUID   `json:"id"`
	PlayerID uuid.UUID   `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

func walletOf(w *wallet.Wallet) walletResponse {
	return walletResponse{
		ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version(),
	}
}

type wagerRequest struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

type wagerResponse struct {
	TransactionID uuid.UUID `json:"transactionId"`
	Status        string    `json:"status"`

	// Balance só existe quando a operação concluiu. Numa recusa não há saldo
	// resultante, e omitir é mais honesto que devolver zero.
	Balance *money.Money `json:"balance,omitempty"`

	FailureCode string `json:"failureCode,omitempty"`
	Correctable *bool  `json:"correctable,omitempty"`

	IdempotentReplay bool `json:"idempotentReplay"`
}

func wagerOf(res app.ProcessWagerResult) wagerResponse {
	out := wagerResponse{
		TransactionID:    res.TransactionID,
		Status:           res.Status.String(),
		IdempotentReplay: res.IdempotentReplay,
	}
	if !res.Balance.IsUninitialized() {
		saldo := res.Balance
		out.Balance = &saldo
	}
	if res.FailureCode != "" {
		corrigivel := res.FailureCode.Correctable()
		out.FailureCode = res.FailureCode.String()
		out.Correctable = &corrigivel
	}
	return out
}

type transactionResponse struct {
	ID                             uuid.UUID    `json:"id"`
	ProviderID                     string       `json:"providerId,omitempty"`
	ExternalTransactionID          string       `json:"externalTransactionId,omitempty"`
	WalletID                       uuid.UUID    `json:"walletId"`
	PlayerID                       uuid.UUID    `json:"playerId"`
	RoundID                        string       `json:"roundId,omitempty"`
	GameID                         string       `json:"gameId,omitempty"`
	Kind                           string       `json:"kind"`
	Status                         string       `json:"status"`
	Money                          money.Money  `json:"money"`
	Balance                        *money.Money `json:"balance,omitempty"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	FailureCode                    string       `json:"failureCode,omitempty"`
	Correctable                    *bool        `json:"correctable,omitempty"`
	CreatedAt                      string       `json:"createdAt"`
	UpdatedAt                      string       `json:"updatedAt"`
}

type ledgerEntryResponse struct {
	ID            uuid.UUID   `json:"id"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	CreatedAt     string      `json:"createdAt"`
}

type ledgerPageResponse struct {
	WalletID uuid.UUID             `json:"walletId"`
	Entries  []ledgerEntryResponse `json:"entries"`

	// NextCursor é opaco de propósito: ele codifica a sequência interna, e o
	// cliente não deve depender do formato nem construir um por conta própria.
	NextCursor string `json:"nextCursor,omitempty"`
}

type reconciliationResponse struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int         `json:"checkedEntries"`
}

// cursorPrefix versiona o cursor. Mudar a ordenação no futuro passa a produzir
// cursores reconhecidamente diferentes, em vez de reinterpretar os antigos com
// outro significado.
const cursorPrefix = "v1:"

func encodeCursor(seq int64) string {
	if seq <= 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(seq, 10)))
}

func decodeCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	bytes, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: cursor malformado", app.ErrInvalidCommand)
	}
	texto, achou := strings.CutPrefix(string(bytes), cursorPrefix)
	if !achou {
		return 0, fmt.Errorf("%w: cursor de versão desconhecida", app.ErrInvalidCommand)
	}
	seq, err := strconv.ParseInt(texto, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("%w: cursor inválido", app.ErrInvalidCommand)
	}
	return seq, nil
}
