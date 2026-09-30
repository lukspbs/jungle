package postgres

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
)

// O mapeamento de Money usa duas colunas: BIGINT em unidades mínimas e CHAR(3)
// com o código ISO 4217. Nenhuma conversão passa por ponto flutuante, e o par
// (valor, moeda) é preservado exatamente na ida e na volta.
//
// A alternativa NUMERIC foi descartada porque exigiria escolher entre scan em
// float64 (proibido) ou em string com reparsing a cada leitura. BIGINT casa
// diretamente com a representação interna do domínio.

// toMoney reconstrói um Money a partir das duas colunas.
func toMoney(minorUnits int64, currencyCode string) (money.Money, error) {
	currency, err := money.NewCurrency(currencyCode)
	if err != nil {
		return money.Money{}, fmt.Errorf("postgres: moeda persistida inválida: %w", err)
	}
	return money.FromMinorUnits(minorUnits, currency)
}

// toOptionalMoney reconstrói um Money possivelmente ausente. Um valor nulo
// devolve o Money não inicializado, que o domínio reconhece como ausência.
func toOptionalMoney(minorUnits *int64, currencyCode string) (money.Money, error) {
	if minorUnits == nil {
		return money.Money{}, nil
	}
	return toMoney(*minorUnits, currencyCode)
}

// fromOptionalMoney converte um Money possivelmente ausente para a coluna
// anulável.
func fromOptionalMoney(m money.Money) *int64 {
	if m.IsUninitialized() {
		return nil
	}
	units := m.MinorUnits()
	return &units
}

// nullableText converte string vazia em NULL. As constraints do schema exigem
// NULL (e não string vazia) nos metadados externos da abertura interna.
func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// text converte a coluna anulável de volta para string.
func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullableUUID converte uuid.Nil em NULL.
func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// uuidOrNil converte a coluna anulável de volta para uuid.UUID.
func uuidOrNil(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// nullableBytes converte slice vazio em NULL.
func nullableBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
