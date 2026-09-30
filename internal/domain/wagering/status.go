package wagering

import "fmt"

// Status é o estado de uma transação.
type Status string

const (
	// Pending marca registro aceito com processamento ainda não concluído.
	Pending Status = "PENDING"

	// PendingReference marca espera por uma referência ainda indisponível.
	PendingReference Status = "PENDING_REFERENCE"

	// Processed marca conclusão com sucesso. Terminal.
	Processed Status = "PROCESSED"

	// Rejected marca recusa por regra de negócio. Terminal.
	Rejected Status = "REJECTED"

	// Failed marca falha permanente de infraestrutura registrada para
	// auditoria. Terminal.
	Failed Status = "FAILED"
)

// transitions é a máquina de estados completa.
//
// Os três estados terminais não aparecem como chave: ausência de entrada é a
// própria regra de que deles não se sai. Um replay consulta o resultado
// persistido em vez de reaplicar a operação.
var transitions = map[Status][]Status{
	Pending:          {PendingReference, Processed, Rejected, Failed},
	PendingReference: {Processed, Rejected, Failed},
}

// ParseStatus converte a forma persistida em Status.
func ParseStatus(s string) (Status, error) {
	switch st := Status(s); st {
	case Pending, PendingReference, Processed, Rejected, Failed:
		return st, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidStatus, s)
	}
}

// String devolve a forma persistida.
func (s Status) String() string { return string(s) }

// IsTerminal informa se o estado é definitivo.
func (s Status) IsTerminal() bool {
	_, transitionable := transitions[s]
	return !transitionable
}

// CanTransitionTo informa se a transição é prevista pela máquina de estados.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}
