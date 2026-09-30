package wallet

import "errors"

// Erros do agregado. Todos classificáveis por errors.Is: uma rejeição de
// negócio nunca vira panic, e a API precisa distingui-las para devolver
// failureCode estável.
var (
	// ErrInvalidID indica identificador ausente ou nulo.
	ErrInvalidID = errors.New("wallet: identificador inválido")

	// ErrInvalidTimestamp indica instante não informado.
	ErrInvalidTimestamp = errors.New("wallet: instante inválido")

	// ErrInvalidVersion indica versão fora do intervalo válido.
	ErrInvalidVersion = errors.New("wallet: versão inválida")

	// ErrNegativeBalance indica saldo negativo, que a carteira nunca admite.
	ErrNegativeBalance = errors.New("wallet: saldo negativo")

	// ErrInsufficientFunds indica débito maior que o saldo disponível.
	// É distinto de ErrNegativeBalance: este é rejeição de negócio esperada,
	// aquele é incoerência de estado.
	ErrInsufficientFunds = errors.New("wallet: saldo insuficiente")

	// ErrCurrencyMismatch indica movimentação em moeda diferente da carteira.
	ErrCurrencyMismatch = errors.New("wallet: moeda da movimentação difere da carteira")

	// ErrNonPositiveAmount indica movimentação de valor zero ou negativo.
	// Uma operação sem efeito financeiro não produz lançamento.
	ErrNonPositiveAmount = errors.New("wallet: movimentação exige valor positivo")

	// ErrInvalidDirection indica direção de lançamento desconhecida.
	ErrInvalidDirection = errors.New("wallet: direção de lançamento inválida")

	// ErrLedgerArithmetic indica lançamento cujo saldo posterior não decorre do
	// saldo anterior pela direção e pelo valor. Só aparece em reidratação de
	// dados corrompidos: o caminho normal constrói o lançamento pelo agregado.
	ErrLedgerArithmetic = errors.New("wallet: lançamento com aritmética incoerente")

	// ErrInconsistentSnapshot indica estado persistido internamente incoerente.
	ErrInconsistentSnapshot = errors.New("wallet: estado persistido incoerente")
)
