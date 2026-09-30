package wagering

import "errors"

// Erros do pacote, todos classificáveis por errors.Is. Rejeição de negócio
// nunca vira panic.
var (
	// ErrInvalidID indica identificador ausente ou nulo.
	ErrInvalidID = errors.New("wagering: identificador inválido")

	// ErrInvalidTimestamp indica instante não informado.
	ErrInvalidTimestamp = errors.New("wagering: instante inválido")

	// ErrMissingField indica campo obrigatório vazio.
	ErrMissingField = errors.New("wagering: campo obrigatório ausente")

	// ErrKindNotAllowed indica tipo inválido para a origem. É o que recusa
	// OPENING vindo de provedor externo.
	ErrKindNotAllowed = errors.New("wagering: tipo não permitido para esta origem")

	// ErrInvalidKind indica tipo desconhecido.
	ErrInvalidKind = errors.New("wagering: tipo desconhecido")

	// ErrInvalidStatus indica estado desconhecido.
	ErrInvalidStatus = errors.New("wagering: estado desconhecido")

	// ErrInvalidFailureCode indica código de falha desconhecido.
	ErrInvalidFailureCode = errors.New("wagering: código de falha desconhecido")

	// ErrAmountPolicy indica valor incompatível com a política do tipo:
	// LOSS exige exatamente zero, os demais exigem valor positivo.
	ErrAmountPolicy = errors.New("wagering: valor incompatível com o tipo da operação")

	// ErrReferencePolicy indica referência externa presente onde não cabe ou
	// ausente onde é obrigatória.
	ErrReferencePolicy = errors.New("wagering: referência externa incompatível com o tipo")

	// ErrTerminalStatus indica tentativa de transição a partir de estado
	// terminal. Um resultado já registrado é definitivo.
	ErrTerminalStatus = errors.New("wagering: transação em estado terminal não admite transição")

	// ErrInvalidTransition indica transição não prevista pela máquina de estados.
	ErrInvalidTransition = errors.New("wagering: transição inválida")

	// ErrInconsistentSnapshot indica estado persistido internamente incoerente.
	ErrInconsistentSnapshot = errors.New("wagering: estado persistido incoerente")
)
