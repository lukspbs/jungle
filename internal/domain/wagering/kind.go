package wagering

import "fmt"

// Source é a origem da transação.
type Source string

const (
	// Internal marca a abertura de carteira, produzida pelo próprio serviço.
	Internal Source = "INTERNAL"
	// External marca as operações recebidas de provedores.
	External Source = "EXTERNAL"
)

// String devolve a forma persistida.
func (s Source) String() string { return string(s) }

// Kind é o tipo da operação.
type Kind string

const (
	// Opening é a abertura interna da carteira. Reservado ao serviço: nunca
	// pode chegar por HTTP ou SQS.
	Opening Kind = "OPENING"

	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

// ParseKind converte a forma externa em Kind.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case Opening, Bet, Win, Loss, Refund, Rollback:
		return k, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidKind, s)
	}
}

// ParseExternalKind converte a forma externa em Kind, recusando OPENING.
//
// É o ponto único que impõe a regra do desafio: a abertura de carteira é
// interna, e um provedor que envie OPENING por HTTP ou SQS é recusado antes de
// qualquer efeito financeiro.
func ParseExternalKind(s string) (Kind, error) {
	k, err := ParseKind(s)
	if err != nil {
		return "", err
	}
	if k.Source() != External {
		return "", fmt.Errorf("%w: %q é de origem interna", ErrKindNotAllowed, k)
	}
	return k, nil
}

// String devolve a forma persistida.
func (k Kind) String() string { return string(k) }

// Source deriva a origem a partir do tipo, em vez de guardá-la em campo
// próprio. Origem e tipo não têm como divergir se um decorre do outro.
func (k Kind) Source() Source {
	if k == Opening {
		return Internal
	}
	return External
}

// IsReversal informa se o tipo desfaz uma operação anterior e portanto exige
// referência.
func (k Kind) IsReversal() bool { return k == Refund || k == Rollback }

// RequiresPositiveAmount informa se o tipo exige valor maior que zero.
// LOSS é a única operação sem movimentação: exige exatamente "0.00".
func (k Kind) RequiresPositiveAmount() bool { return k != Loss }

// MovesBalance informa se o tipo altera o saldo da carteira.
func (k Kind) MovesBalance() bool { return k != Loss }
