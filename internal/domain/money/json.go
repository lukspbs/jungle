package money

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// dto é a forma de transporte do contrato externo. A ordem dos campos é a
// ordem de declaração, o que torna o MarshalJSON determinístico e utilizável
// como entrada do hash de idempotência.
type dto struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON emite {"amount":"25.00","currency":"BRL"}.
//
// O montante é sempre uma string decimal: emitir número JSON exporia o valor
// a conversão para float64 do outro lado do fio.
func (m Money) MarshalJSON() ([]byte, error) {
	if m.IsUninitialized() {
		return nil, ErrUninitialized
	}
	return json.Marshal(dto{Amount: m.String(), Currency: m.currency.String()})
}

// UnmarshalJSON lê o contrato externo aplicando as regras de entrada: o
// montante precisa ser string no formato canônico, com no máximo Scale casas
// e sem sinal negativo.
//
// A assimetria com MarshalJSON é intencional. Money negativo existe como
// resultado interno de Sub e Neg e é serializável (a reconciliação publica
// difference negativo), mas jamais é aceito como entrada financeira externa.
func (m *Money) UnmarshalJSON(data []byte) error {
	var raw dto
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		// Um número JSON no campo amount cai aqui: o destino é string.
		return fmt.Errorf("%w: %s", ErrInvalidAmount, err)
	}
	parsed, err := Parse(raw.Amount, raw.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}
