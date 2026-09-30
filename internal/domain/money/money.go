// Package money implementa o value object monetário do domínio.
//
// # Representação
//
// Money guarda um int64 em unidades mínimas (centavos) e a moeda. Nenhum
// float32 ou float64 participa de parsing, cálculo, serialização ou
// persistência — nem sequer de forma intermediária.
//
// A escala é fixa em 2 casas para todas as moedas. Moedas de escala diferente
// (JPY com 0, BHD com 3) não são suportadas neste desafio; a limitação está
// registrada em ARCHITECTURE.md.
//
// # Limites
//
// O intervalo representável é [-92.233.720.368.547.758,08 ..
// 92.233.720.368.547.758,07]. Parsing, soma, subtração e negação detectam
// estouro e devolvem ErrOverflow em vez de truncar.
//
// # Normalização
//
// "25", "25.0" e "25.00" são formas equivalentes e todas produzem 2500
// unidades mínimas. A forma canônica emitida por String e MarshalJSON tem
// sempre exatamente 2 casas. O hash de idempotência é calculado sobre essa
// forma canônica, de modo que formas equivalentes na entrada não geram
// conflito de payload.
//
// # Sinal
//
// Parse rejeita negativos: todo caminho que chama Parse é uma fronteira de
// entrada externa, onde valores negativos são inválidos. Valores negativos
// existem internamente como resultado de Sub e Neg (diferenças, reconciliação)
// e são reidratados do banco por FromMinorUnits.
package money

import (
	"fmt"
	"math"
	"strings"
)

// Scale é a quantidade de casas decimais da representação canônica.
const Scale = 2

// scaleFactor é 10^Scale: o fator entre unidade principal e unidade mínima.
const scaleFactor int64 = 100

// Money é um valor monetário imutável. Todos os métodos têm receiver por valor
// e devolvem novos Money; nenhuma operação muta o receiver.
type Money struct {
	minorUnits int64
	currency   Currency
}

// Parse constrói um Money a partir de uma string decimal e de um código de
// moeda, aplicando as regras de entrada externa.
//
// Aceita apenas dígitos com um separador decimal opcional e no máximo Scale
// casas. Rejeita string vazia, NaN, Infinity, notação científica, sinal
// explícito, espaços, separador de milhar, escala excedente e negativos.
func Parse(amount, currencyCode string) (Money, error) {
	cur, err := NewCurrency(currencyCode)
	if err != nil {
		return Money{}, err
	}
	units, err := parseMinorUnits(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minorUnits: units, currency: cur}, nil
}

// FromMinorUnits reidrata um Money a partir da persistência. Aceita qualquer
// sinal: o banco é fonte confiável e diferenças internas podem ser negativas.
func FromMinorUnits(units int64, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrUninitialized
	}
	return Money{minorUnits: units, currency: currency}, nil
}

// Zero devolve o valor nulo da moeda informada.
func Zero(currency Currency) (Money, error) {
	return FromMinorUnits(0, currency)
}

// MinorUnits expõe a representação inteira para persistência em BIGINT.
func (m Money) MinorUnits() int64 { return m.minorUnits }

// Currency devolve a moeda do valor.
func (m Money) Currency() Currency { return m.currency }

// IsUninitialized informa se o Money é o zero-value da struct, que não é um
// valor monetário válido por não carregar moeda.
func (m Money) IsUninitialized() bool { return m.currency.IsZero() }

// IsZero informa se o valor é exatamente zero. Um Money não inicializado não
// é considerado zero: ele não é um valor.
func (m Money) IsZero() bool { return !m.IsUninitialized() && m.minorUnits == 0 }

// IsPositive informa se o valor é maior que zero.
func (m Money) IsPositive() bool { return !m.IsUninitialized() && m.minorUnits > 0 }

// IsNegative informa se o valor é menor que zero.
func (m Money) IsNegative() bool { return !m.IsUninitialized() && m.minorUnits < 0 }

// Add soma dois valores da mesma moeda.
func (m Money) Add(other Money) (Money, error) {
	if err := m.requireCompatible(other); err != nil {
		return Money{}, err
	}
	a, b := m.minorUnits, other.minorUnits
	if b > 0 && a > math.MaxInt64-b {
		return Money{}, fmt.Errorf("%w: soma", ErrOverflow)
	}
	if b < 0 && a < math.MinInt64-b {
		return Money{}, fmt.Errorf("%w: soma", ErrOverflow)
	}
	return Money{minorUnits: a + b, currency: m.currency}, nil
}

// Sub subtrai dois valores da mesma moeda. O resultado pode ser negativo.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.requireCompatible(other); err != nil {
		return Money{}, err
	}
	a, b := m.minorUnits, other.minorUnits
	if b < 0 && a > math.MaxInt64+b {
		return Money{}, fmt.Errorf("%w: subtração", ErrOverflow)
	}
	if b > 0 && a < math.MinInt64+b {
		return Money{}, fmt.Errorf("%w: subtração", ErrOverflow)
	}
	return Money{minorUnits: a - b, currency: m.currency}, nil
}

// Neg devolve o valor simétrico.
func (m Money) Neg() (Money, error) {
	if m.IsUninitialized() {
		return Money{}, ErrUninitialized
	}
	if m.minorUnits == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: negação", ErrOverflow)
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

// Abs devolve o valor absoluto.
func (m Money) Abs() (Money, error) {
	if m.IsNegative() {
		return m.Neg()
	}
	if m.IsUninitialized() {
		return Money{}, ErrUninitialized
	}
	return m, nil
}

// Cmp compara dois valores da mesma moeda, devolvendo -1, 0 ou 1.
func (m Money) Cmp(other Money) (int, error) {
	if err := m.requireCompatible(other); err != nil {
		return 0, err
	}
	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal informa se dois valores têm mesma moeda e mesmo montante. Moedas
// distintas nunca são iguais; dois Money não inicializados também não são.
func (m Money) Equal(other Money) bool {
	if m.IsUninitialized() || other.IsUninitialized() {
		return false
	}
	return m.minorUnits == other.minorUnits && m.currency == other.currency
}

// String devolve a forma decimal canônica, sempre com Scale casas.
func (m Money) String() string {
	if m.IsUninitialized() {
		return "<money inválido>"
	}
	return formatMinorUnits(m.minorUnits)
}

// requireCompatible valida que ambos os operandos são utilizáveis e partilham
// a moeda.
func (m Money) requireCompatible(other Money) error {
	if m.IsUninitialized() || other.IsUninitialized() {
		return ErrUninitialized
	}
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s e %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

// parseMinorUnits converte a string decimal em unidades mínimas sem passar por
// ponto flutuante em nenhum momento.
func parseMinorUnits(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("%w: string vazia", ErrInvalidAmount)
	}
	// Distinguimos o negativo bem-formado do lixo sintático: a API precisa de
	// failureCodes diferentes para "mandou -25.00" e "mandou abc".
	if s[0] == '-' {
		return 0, fmt.Errorf("%w: %q", ErrNegativeAmount, s)
	}

	if strings.Count(s, ".") > 1 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}

	intPart, fracPart, hasSeparator := strings.Cut(s, ".")
	if intPart == "" {
		// Cobre "." e ".50": exigimos ao menos um dígito na parte inteira.
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	if hasSeparator && fracPart == "" {
		// Cobre "25.".
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}

	// O conjunto de caracteres é validado antes da escala: "2.5e2" e "25.00 "
	// são entrada inválida, não escala excedida. Essa varredura também é o que
	// rejeita "NaN", "Infinity", separador de milhar, espaços e sinais.
	if !onlyDigits(intPart) || !onlyDigits(fracPart) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	if len(fracPart) > Scale {
		return 0, fmt.Errorf("%w: %q tem mais de %d casas", ErrScaleExceeded, s, Scale)
	}

	units, err := accumulateDigits(intPart, s)
	if err != nil {
		return 0, err
	}

	// Desloca a parte inteira para unidades mínimas.
	if units > math.MaxInt64/scaleFactor {
		return 0, fmt.Errorf("%w: %q excede o intervalo representável", ErrOverflow, s)
	}
	units *= scaleFactor

	if hasSeparator {
		// Completa a escala à direita: "25.5" vale 50 centavos, não 5.
		frac, err := accumulateDigits(fracPart+strings.Repeat("0", Scale-len(fracPart)), s)
		if err != nil {
			return 0, err
		}
		if units > math.MaxInt64-frac {
			return 0, fmt.Errorf("%w: %q excede o intervalo representável", ErrOverflow, s)
		}
		units += frac
	}

	return units, nil
}

// onlyDigits informa se a string é composta apenas por dígitos ASCII. A string
// vazia é aceita: ela representa a ausência de parte fracionária.
func onlyDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// accumulateDigits converte uma sequência de dígitos já validada em int64,
// detectando overflow a cada passo.
func accumulateDigits(digits, original string) (int64, error) {
	var acc int64
	for i := 0; i < len(digits); i++ {
		d := int64(digits[i] - '0')
		if acc > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("%w: %q excede o intervalo representável", ErrOverflow, original)
		}
		acc = acc*10 + d
	}
	return acc, nil
}

// formatMinorUnits produz a forma canônica. A conversão para o valor absoluto
// passa por uint64 para que math.MinInt64 não estoure.
func formatMinorUnits(v int64) string {
	magnitude := uint64(v)
	sign := ""
	if v < 0 {
		magnitude = -uint64(v)
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d", sign, magnitude/uint64(scaleFactor), Scale, magnitude%uint64(scaleFactor))
}
