package money

import "fmt"

// Currency é um código de moeda ISO 4217 validado.
//
// A validação é de formato: exatamente três letras maiúsculas ASCII. Optei
// por não embutir a tabela ISO 4217 completa para não precisar versioná-la;
// a consequência é que um código sintaticamente válido mas inexistente seria
// aceito. O risco é contido porque a moeda de toda movimentação é comparada
// com a moeda da carteira, que por sua vez nasce de uma abertura controlada.
type Currency struct {
	code string
}

// NewCurrency valida e constrói um Currency.
func NewCurrency(code string) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 'A' || code[i] > 'Z' {
			return Currency{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
		}
	}
	return Currency{code: code}, nil
}

// MustCurrency é o atalho para constantes de teste e bootstrap. Entra em panic
// apenas em erro de programação, nunca em rejeição de negócio.
func MustCurrency(code string) Currency {
	c, err := NewCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

// IsZero informa se o Currency é o zero-value, isto é, não inicializado.
func (c Currency) IsZero() bool { return c.code == "" }

// String devolve o código ISO 4217.
func (c Currency) String() string { return c.code }
