package wagering

import (
	"bytes"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
)

// Snapshot é a forma persistida de uma transação.
type Snapshot struct {
	ID       uuid.UUID
	Kind     Kind
	Status   Status
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Amount   money.Money

	ProviderID            string
	ExternalTransactionID string
	IdempotencyKey        string
	PayloadHash           []byte
	RoundID               string
	GameID                string

	ReferenceExternalTransactionID string
	ReferenceTransactionID         uuid.UUID

	ResultBalance money.Money
	FailureCode   FailureCode

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Rehydrate reconstrói a transação a partir do estado persistido.
//
// Reidratação não reaplica a operação, não dispara transição e não emite
// evento: ela restaura o estado e recusa o que o domínio não poderia ter
// produzido. As validações espelham de propósito as constraints do schema —
// se um dado escapar por um caminho de escrita novo, ele é barrado na leitura.
func Rehydrate(s Snapshot) (*WagerTransaction, error) {
	if s.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: transação sem id", ErrInvalidID)
	}
	if s.WalletID == uuid.Nil {
		return nil, fmt.Errorf("%w: transação sem carteira", ErrInvalidID)
	}
	if s.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: transação sem jogador", ErrInvalidID)
	}
	if _, err := ParseKind(s.Kind.String()); err != nil {
		return nil, err
	}
	if _, err := ParseStatus(s.Status.String()); err != nil {
		return nil, err
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("%w: transação sem instantes", ErrInvalidTimestamp)
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return nil, fmt.Errorf("%w: atualização anterior à criação", ErrInconsistentSnapshot)
	}
	if err := validateAmountPolicy(s.Kind, s.Amount); err != nil {
		return nil, err
	}
	if err := validateSnapshotMetadata(s); err != nil {
		return nil, err
	}
	if err := validateSnapshotOutcome(s); err != nil {
		return nil, err
	}

	return &WagerTransaction{
		id:                             s.ID,
		kind:                           s.Kind,
		status:                         s.Status,
		walletID:                       s.WalletID,
		playerID:                       s.PlayerID,
		amount:                         s.Amount,
		providerID:                     s.ProviderID,
		externalTransactionID:          s.ExternalTransactionID,
		idempotencyKey:                 s.IdempotencyKey,
		payloadHash:                    bytes.Clone(s.PayloadHash),
		roundID:                        s.RoundID,
		gameID:                         s.GameID,
		referenceExternalTransactionID: s.ReferenceExternalTransactionID,
		referenceTransactionID:         s.ReferenceTransactionID,
		resultBalance:                  s.ResultBalance,
		failureCode:                    s.FailureCode,
		createdAt:                      s.CreatedAt,
		updatedAt:                      s.UpdatedAt,
	}, nil
}

// validateSnapshotMetadata impõe que a operação externa traga seus metadados e
// que a abertura interna não os traga.
func validateSnapshotMetadata(s Snapshot) error {
	if s.Kind.Source() == External {
		for field, value := range map[string]string{
			"providerId":            s.ProviderID,
			"externalTransactionId": s.ExternalTransactionID,
			"idempotencyKey":        s.IdempotencyKey,
			"roundId":               s.RoundID,
			"gameId":                s.GameID,
		} {
			if value == "" {
				return fmt.Errorf("%w: operação externa sem %s", ErrMissingField, field)
			}
		}
		if len(s.PayloadHash) == 0 {
			return fmt.Errorf("%w: operação externa sem payloadHash", ErrMissingField)
		}
		return validateReferencePolicy(s.Kind, s.ReferenceExternalTransactionID)
	}

	if s.ProviderID != "" || s.ExternalTransactionID != "" || s.IdempotencyKey != "" ||
		len(s.PayloadHash) > 0 || s.RoundID != "" || s.GameID != "" ||
		s.ReferenceExternalTransactionID != "" || s.ReferenceTransactionID != uuid.Nil {
		return fmt.Errorf("%w: abertura interna com metadados externos", ErrInconsistentSnapshot)
	}
	return nil
}

// validateSnapshotOutcome impõe a correspondência entre estado e desfecho:
// saldo congelado existe exatamente em PROCESSED, e código de falha existe
// exatamente em REJECTED e FAILED.
func validateSnapshotOutcome(s Snapshot) error {
	if s.Status == Processed {
		if s.ResultBalance.IsUninitialized() {
			return fmt.Errorf("%w: PROCESSED sem saldo resultante", ErrInconsistentSnapshot)
		}
		if s.ResultBalance.IsNegative() {
			return fmt.Errorf("%w: saldo resultante de %s", ErrInconsistentSnapshot, s.ResultBalance)
		}
		if s.ResultBalance.Currency() != s.Amount.Currency() {
			return fmt.Errorf("%w: saldo em %s para operação em %s",
				money.ErrCurrencyMismatch, s.ResultBalance.Currency(), s.Amount.Currency())
		}
	} else if !s.ResultBalance.IsUninitialized() {
		return fmt.Errorf("%w: %s com saldo resultante", ErrInconsistentSnapshot, s.Status)
	}

	terminalComFalha := s.Status == Rejected || s.Status == Failed
	if terminalComFalha {
		if !s.FailureCode.IsKnown() {
			return fmt.Errorf("%w: %s sem código de falha conhecido", ErrInconsistentSnapshot, s.Status)
		}
	} else if s.FailureCode != "" {
		return fmt.Errorf("%w: %s com código de falha %q", ErrInconsistentSnapshot, s.Status, s.FailureCode)
	}
	return nil
}
