package wagering

import "fmt"

// FailureCode é o motivo estável de uma rejeição ou falha, devolvido ao
// provedor e persistido para auditoria.
//
// Cada código declara se a entrada é corrigível. Corrigível significa que o
// provedor pode ajustar o corpo da requisição e reenviar sob uma chave nova
// com chance de sucesso. Não corrigível é resultado definitivo de negócio:
// reenviar o mesmo pedido corrigido não muda o desfecho.
type FailureCode string

const (
	// FailureInsufficientFunds: aposta maior que o saldo disponível.
	FailureInsufficientFunds FailureCode = "INSUFFICIENT_FUNDS"

	// FailureReversalInsufficientFunds: reversão que precisaria debitar mais
	// que o saldo disponível. Deliberadamente distinto de
	// FailureInsufficientFunds, conforme exigido pelo desafio: a causa e a
	// ação corretiva do operador são diferentes.
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"

	// FailureReferenceNotFound: a referência não chegou dentro do prazo.
	FailureReferenceNotFound FailureCode = "REFERENCE_NOT_FOUND"

	// FailureReferenceNotProcessed: a referência existe mas não terminou com
	// sucesso, então não há o que reverter.
	FailureReferenceNotProcessed FailureCode = "REFERENCE_NOT_PROCESSED"

	// FailureReferenceMismatch: a operação e a referência discordam em
	// provedor, jogador, carteira, moeda ou rodada.
	FailureReferenceMismatch FailureCode = "REFERENCE_MISMATCH"

	// FailureReferenceAmountMismatch: o valor da reversão difere do valor
	// referenciado. Reversões parciais estão fora do escopo.
	FailureReferenceAmountMismatch FailureCode = "REFERENCE_AMOUNT_MISMATCH"

	// FailureReferenceAlreadyReversed: a referência já recebeu uma reversão
	// bem-sucedida deste tipo.
	FailureReferenceAlreadyReversed FailureCode = "REFERENCE_ALREADY_REVERSED"

	// FailureWalletNotFound: a carteira informada não existe.
	FailureWalletNotFound FailureCode = "WALLET_NOT_FOUND"

	// FailureWalletPlayerMismatch: a carteira não pertence ao jogador informado.
	FailureWalletPlayerMismatch FailureCode = "WALLET_PLAYER_MISMATCH"

	// FailureCurrencyMismatch: a moeda da operação difere da moeda da carteira.
	FailureCurrencyMismatch FailureCode = "CURRENCY_MISMATCH"

	// FailureInvalidAmount: valor incompatível com a política do tipo.
	FailureInvalidAmount FailureCode = "INVALID_AMOUNT"

	// FailureKindNotAllowed: tipo não aceito nesta origem, como OPENING vindo
	// de um provedor.
	FailureKindNotAllowed FailureCode = "KIND_NOT_ALLOWED"

	// FailureInfrastructure: falha permanente de infraestrutura, registrada
	// para auditoria depois de esgotadas as tentativas.
	FailureInfrastructure FailureCode = "INFRASTRUCTURE_FAILURE"
)

// correctable mapeia cada código à sua natureza. Um código ausente do mapa não
// existe: é assim que ParseFailureCode recusa valor desconhecido.
var correctable = map[FailureCode]bool{
	FailureInsufficientFunds:         false,
	FailureReversalInsufficientFunds: false,
	FailureReferenceNotFound:         false,
	FailureReferenceNotProcessed:     false,
	FailureReferenceAlreadyReversed:  false,
	FailureInfrastructure:            false,

	FailureReferenceMismatch:       true,
	FailureReferenceAmountMismatch: true,
	FailureWalletNotFound:          true,
	FailureWalletPlayerMismatch:    true,
	FailureCurrencyMismatch:        true,
	FailureInvalidAmount:           true,
	FailureKindNotAllowed:          true,
}

// ParseFailureCode converte a forma persistida em FailureCode.
func ParseFailureCode(s string) (FailureCode, error) {
	c := FailureCode(s)
	if _, known := correctable[c]; !known {
		return "", fmt.Errorf("%w: %q", ErrInvalidFailureCode, s)
	}
	return c, nil
}

// String devolve a forma persistida.
func (c FailureCode) String() string { return string(c) }

// Correctable informa se o provedor pode corrigir a entrada e reenviar sob
// chave nova com chance de sucesso.
func (c FailureCode) Correctable() bool { return correctable[c] }

// IsKnown informa se o código pertence ao conjunto documentado.
func (c FailureCode) IsKnown() bool {
	_, known := correctable[c]
	return known
}
