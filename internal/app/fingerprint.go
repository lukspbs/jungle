package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
)

// fingerprintVersion identifica o esquema do hash.
//
// Entra no material hasheado para que uma mudança futura no conjunto de campos
// produza hashes reconhecidamente diferentes, em vez de colidir em silêncio com
// os antigos.
const fingerprintVersion = "1"

// Fingerprint é o hash determinístico dos campos de negócio de uma operação.
//
// # Algoritmo
//
// SHA-256 sobre JSON canônico. O JSON é produzido a partir de um map, e a
// biblioteca padrão do Go ordena as chaves de um map lexicograficamente ao
// serializar — é daí que vem a canonicidade, sem depender da ordem em que os
// campos foram declarados em alguma struct.
//
// # Campos
//
// Entram providerId, externalTransactionId, playerId, walletId, roundId,
// gameId, kind, amount, currency e referenceExternalTransactionId.
//
// Ficam de fora a chave de idempotência e todo metadado de transporte —
// messageId, cabeçalhos HTTP, occurredAt do envelope SQS, atributos da
// mensagem. É isso que faz a mesma operação chegar por HTTP e por SQS com o
// mesmo hash: o que não é campo de negócio não participa.
//
// # Normalizações
//
// O valor monetário entra na forma canônica de Money, com escala fixa de duas
// casas. "25", "25.0" e "25.00" produzem o mesmo material e, portanto, o mesmo
// hash: variação de formato na entrada não vira conflito de payload.
//
// Os identificadores entram na forma canônica do UUID, em minúsculas. Os
// campos ausentes entram como string vazia em vez de serem omitidos, de modo
// que presença e ausência nunca ficam ambíguas.
type Fingerprint [sha256.Size]byte

// FingerprintFields reúne os campos de negócio de uma operação.
type FingerprintFields struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
}

// ComputeFingerprint calcula o hash dos campos de negócio.
func ComputeFingerprint(f FingerprintFields) (Fingerprint, error) {
	if f.Money.IsUninitialized() {
		return Fingerprint{}, fmt.Errorf("%w: valor sem moeda no cálculo do hash", ErrInvalidCommand)
	}

	material, err := json.Marshal(map[string]string{
		"v":                              fingerprintVersion,
		"providerId":                     f.ProviderID,
		"externalTransactionId":          f.ExternalTransactionID,
		"playerId":                       f.PlayerID.String(),
		"walletId":                       f.WalletID.String(),
		"roundId":                        f.RoundID,
		"gameId":                         f.GameID,
		"kind":                           f.Kind.String(),
		"amount":                         f.Money.String(),
		"currency":                       f.Money.Currency().String(),
		"referenceExternalTransactionId": f.ReferenceExternalTransactionID,
	})
	if err != nil {
		return Fingerprint{}, fmt.Errorf("app: falha ao montar o material do hash: %w", err)
	}

	return sha256.Sum256(material), nil
}

// Bytes devolve o hash para persistência em BYTEA.
func (f Fingerprint) Bytes() []byte { return f[:] }

// String devolve o hash em hexadecimal, para logs e diagnóstico.
func (f Fingerprint) String() string { return hex.EncodeToString(f[:]) }

// Equal compara dois hashes.
func (f Fingerprint) Equal(other []byte) bool {
	if len(other) != len(f) {
		return false
	}
	for i := range f {
		if f[i] != other[i] {
			return false
		}
	}
	return true
}
