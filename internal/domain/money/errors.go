package money

import "errors"

// Erros do pacote. Todos são classificáveis por errors.Is, conforme exigido
// pelo domínio: nenhuma rejeição de negócio usa panic.
var (
	// ErrUninitialized indica uso de um Money zero-value (sem moeda definida).
	ErrUninitialized = errors.New("money: valor não inicializado")

	// ErrInvalidCurrency indica código de moeda fora do formato ISO 4217.
	ErrInvalidCurrency = errors.New("money: moeda inválida")

	// ErrCurrencyMismatch indica aritmética ou comparação entre moedas distintas.
	ErrCurrencyMismatch = errors.New("money: moedas incompatíveis")

	// ErrInvalidAmount indica valor fora do formato decimal canônico. Cobre
	// string vazia, NaN, Infinity, notação científica, sinal de mais,
	// separadores não decimais e espaços.
	ErrInvalidAmount = errors.New("money: valor decimal inválido")

	// ErrScaleExceeded indica mais casas decimais do que a escala suportada.
	// Nunca arredondo silenciosamente: a entrada é rejeitada.
	ErrScaleExceeded = errors.New("money: escala excedida")

	// ErrNegativeAmount indica valor negativo em entrada financeira externa.
	ErrNegativeAmount = errors.New("money: valor negativo não permitido na entrada")

	// ErrOverflow indica estouro do intervalo representável em int64.
	ErrOverflow = errors.New("money: overflow no intervalo representável")
)
