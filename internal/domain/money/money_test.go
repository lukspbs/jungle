package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/lukspbs/jungle/internal/domain/money"
)

var (
	brl = money.MustCurrency("BRL")
	usd = money.MustCurrency("USD")
)

func mustParse(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("Parse(%q, %q) devolveu erro inesperado: %v", amount, currency, err)
	}
	return m
}

func TestParseValores(t *testing.T) {
	tests := []struct {
		name   string
		amount string
		want   int64
	}{
		{"escala canônica", "25.00", 2500},
		{"sem separador", "25", 2500},
		{"uma casa decimal", "25.0", 2500},
		{"uma casa significativa", "25.5", 2550},
		{"zero canônico", "0.00", 0},
		{"zero sem separador", "0", 0},
		{"menor unidade", "0.01", 1},
		{"zeros à esquerda", "0025.00", 2500},
		{"valor de abertura", "1000.00", 100000},
		{"máximo representável", "92233720368547758.07", math.MaxInt64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mustParse(t, tt.amount, "BRL")
			if got := m.MinorUnits(); got != tt.want {
				t.Errorf("MinorUnits() = %d, esperado %d", got, tt.want)
			}
			if m.Currency() != brl {
				t.Errorf("Currency() = %v, esperado BRL", m.Currency())
			}
		})
	}
}

func TestParseNormalizaFormasEquivalentes(t *testing.T) {
	// A equivalência precisa valer na forma canônica, que é o que entra no
	// hash de idempotência: "25", "25.0" e "25.00" não podem gerar conflito.
	formas := []string{"25", "25.0", "25.00", "0025.00"}
	canonico := "25.00"

	for _, forma := range formas {
		m := mustParse(t, forma, "BRL")
		if got := m.String(); got != canonico {
			t.Errorf("Parse(%q).String() = %q, esperado %q", forma, got, canonico)
		}
	}
}

func TestParseRejeitaEntradasInvalidas(t *testing.T) {
	tests := []struct {
		name    string
		amount  string
		wantErr error
	}{
		{"string vazia", "", money.ErrInvalidAmount},
		{"espaço à esquerda", " 25.00", money.ErrInvalidAmount},
		{"espaço à direita", "25.00 ", money.ErrInvalidAmount},
		{"vírgula decimal", "25,00", money.ErrInvalidAmount},
		{"separador de milhar", "1,000.00", money.ErrInvalidAmount},
		{"NaN", "NaN", money.ErrInvalidAmount},
		{"Infinity", "Infinity", money.ErrInvalidAmount},
		{"inf", "inf", money.ErrInvalidAmount},
		{"notação científica minúscula", "1e5", money.ErrInvalidAmount},
		{"notação científica maiúscula", "1E5", money.ErrInvalidAmount},
		{"notação científica com decimal", "2.5e2", money.ErrInvalidAmount},
		{"sinal de mais explícito", "+25.00", money.ErrInvalidAmount},
		{"apenas separador", ".", money.ErrInvalidAmount},
		{"sem parte inteira", ".50", money.ErrInvalidAmount},
		{"sem parte fracionária", "25.", money.ErrInvalidAmount},
		{"dois separadores", "25.00.00", money.ErrInvalidAmount},
		{"hexadecimal", "0x19", money.ErrInvalidAmount},
		{"texto", "abc", money.ErrInvalidAmount},

		{"escala excedida", "25.001", money.ErrScaleExceeded},
		{"escala excedida com zeros", "0.000", money.ErrScaleExceeded},

		{"negativo", "-25.00", money.ErrNegativeAmount},
		{"negativo mínimo", "-0.01", money.ErrNegativeAmount},
		{"zero negativo", "-0.00", money.ErrNegativeAmount},

		{"acima do máximo", "92233720368547758.08", money.ErrOverflow},
		{"muito acima do máximo", "99999999999999999999.99", money.ErrOverflow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := money.Parse(tt.amount, "BRL")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Parse(%q) devolveu erro %v, esperado %v", tt.amount, err, tt.wantErr)
			}
			// Nenhuma entrada inválida pode ser arredondada silenciosamente.
			if !m.IsUninitialized() {
				t.Errorf("Parse(%q) devolveu Money utilizável %v junto com o erro", tt.amount, m)
			}
		})
	}
}

func TestCurrencyValidacao(t *testing.T) {
	invalidas := []string{"", "B", "BR", "BRLL", "brl", "Brl", "B R", "12 3", "R$"}
	for _, code := range invalidas {
		t.Run("rejeita "+code, func(t *testing.T) {
			if _, err := money.NewCurrency(code); !errors.Is(err, money.ErrInvalidCurrency) {
				t.Errorf("NewCurrency(%q) devolveu %v, esperado ErrInvalidCurrency", code, err)
			}
		})
	}

	for _, code := range []string{"BRL", "USD", "EUR"} {
		if c, err := money.NewCurrency(code); err != nil || c.String() != code {
			t.Errorf("NewCurrency(%q) = (%v, %v), esperado sucesso", code, c, err)
		}
	}
}

func TestOperacoesExigemMoedasCompativeis(t *testing.T) {
	real := mustParse(t, "10.00", "BRL")
	dolar := mustParse(t, "10.00", "USD")

	t.Run("Add", func(t *testing.T) {
		if _, err := real.Add(dolar); !errors.Is(err, money.ErrCurrencyMismatch) {
			t.Errorf("Add devolveu %v, esperado ErrCurrencyMismatch", err)
		}
	})
	t.Run("Sub", func(t *testing.T) {
		if _, err := real.Sub(dolar); !errors.Is(err, money.ErrCurrencyMismatch) {
			t.Errorf("Sub devolveu %v, esperado ErrCurrencyMismatch", err)
		}
	})
	t.Run("Cmp", func(t *testing.T) {
		if _, err := real.Cmp(dolar); !errors.Is(err, money.ErrCurrencyMismatch) {
			t.Errorf("Cmp devolveu %v, esperado ErrCurrencyMismatch", err)
		}
	})
	t.Run("Equal", func(t *testing.T) {
		if real.Equal(dolar) {
			t.Error("Equal entre moedas distintas devolveu true")
		}
	})
}

func TestAritmetica(t *testing.T) {
	t.Run("soma", func(t *testing.T) {
		got, err := mustParse(t, "100.00", "BRL").Add(mustParse(t, "25.50", "BRL"))
		if err != nil {
			t.Fatalf("Add devolveu erro: %v", err)
		}
		if got.String() != "125.50" {
			t.Errorf("Add = %q, esperado \"125.50\"", got)
		}
	})

	t.Run("subtração", func(t *testing.T) {
		got, err := mustParse(t, "100.00", "BRL").Sub(mustParse(t, "80.00", "BRL"))
		if err != nil {
			t.Fatalf("Sub devolveu erro: %v", err)
		}
		if got.String() != "20.00" {
			t.Errorf("Sub = %q, esperado \"20.00\"", got)
		}
	})

	t.Run("subtração pode ficar negativa", func(t *testing.T) {
		// Diferenças internas admitem sinal negativo; o saldo da carteira não.
		got, err := mustParse(t, "20.00", "BRL").Sub(mustParse(t, "80.00", "BRL"))
		if err != nil {
			t.Fatalf("Sub devolveu erro: %v", err)
		}
		if !got.IsNegative() || got.String() != "-60.00" {
			t.Errorf("Sub = %q, esperado \"-60.00\" negativo", got)
		}
	})

	t.Run("negação", func(t *testing.T) {
		got, err := mustParse(t, "25.00", "BRL").Neg()
		if err != nil {
			t.Fatalf("Neg devolveu erro: %v", err)
		}
		if got.String() != "-25.00" {
			t.Errorf("Neg = %q, esperado \"-25.00\"", got)
		}
	})

	t.Run("valor absoluto", func(t *testing.T) {
		negativo, err := mustParse(t, "25.00", "BRL").Neg()
		if err != nil {
			t.Fatalf("Neg devolveu erro: %v", err)
		}
		got, err := negativo.Abs()
		if err != nil {
			t.Fatalf("Abs devolveu erro: %v", err)
		}
		if got.String() != "25.00" {
			t.Errorf("Abs = %q, esperado \"25.00\"", got)
		}
	})

	t.Run("centavos não se perdem em cadeia", func(t *testing.T) {
		// Em float64 esta sequência escorrega; em int64 tem que fechar em zero.
		acc := mustParse(t, "0.00", "BRL")
		for i := 0; i < 10; i++ {
			var err error
			if acc, err = acc.Add(mustParse(t, "0.10", "BRL")); err != nil {
				t.Fatalf("Add devolveu erro: %v", err)
			}
		}
		if acc.String() != "1.00" {
			t.Fatalf("soma de 10 × 0.10 = %q, esperado \"1.00\"", acc)
		}
		resto, err := acc.Sub(mustParse(t, "1.00", "BRL"))
		if err != nil {
			t.Fatalf("Sub devolveu erro: %v", err)
		}
		if !resto.IsZero() {
			t.Errorf("resto = %q, esperado zero exato", resto)
		}
	})
}

func TestOverflowNasOperacoes(t *testing.T) {
	maxBRL, err := money.FromMinorUnits(math.MaxInt64, brl)
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}
	minBRL, err := money.FromMinorUnits(math.MinInt64, brl)
	if err != nil {
		t.Fatalf("FromMinorUnits devolveu erro: %v", err)
	}
	umCentavo := mustParse(t, "0.01", "BRL")

	t.Run("soma estoura", func(t *testing.T) {
		if _, err := maxBRL.Add(umCentavo); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Add devolveu %v, esperado ErrOverflow", err)
		}
	})
	t.Run("subtração estoura", func(t *testing.T) {
		if _, err := minBRL.Sub(umCentavo); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Sub devolveu %v, esperado ErrOverflow", err)
		}
	})
	t.Run("negação do mínimo estoura", func(t *testing.T) {
		if _, err := minBRL.Neg(); !errors.Is(err, money.ErrOverflow) {
			t.Errorf("Neg devolveu %v, esperado ErrOverflow", err)
		}
	})
	t.Run("formata o mínimo sem estourar", func(t *testing.T) {
		if got := minBRL.String(); got != "-92233720368547758.08" {
			t.Errorf("String() = %q, esperado \"-92233720368547758.08\"", got)
		}
	})
}

func TestZeroValueRejeitado(t *testing.T) {
	var vazio money.Money
	valido := mustParse(t, "10.00", "BRL")

	if !vazio.IsUninitialized() {
		t.Fatal("Money zero-value deveria ser reportado como não inicializado")
	}
	if vazio.IsZero() || vazio.IsPositive() || vazio.IsNegative() {
		t.Error("Money não inicializado não pode responder a predicados de sinal")
	}

	t.Run("Add", func(t *testing.T) {
		if _, err := valido.Add(vazio); !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("Add devolveu %v, esperado ErrUninitialized", err)
		}
	})
	t.Run("Neg", func(t *testing.T) {
		if _, err := vazio.Neg(); !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("Neg devolveu %v, esperado ErrUninitialized", err)
		}
	})
	t.Run("FromMinorUnits sem moeda", func(t *testing.T) {
		if _, err := money.FromMinorUnits(100, money.Currency{}); !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("FromMinorUnits devolveu %v, esperado ErrUninitialized", err)
		}
	})
	t.Run("Equal", func(t *testing.T) {
		if vazio.Equal(vazio) {
			t.Error("dois Money não inicializados não podem ser iguais")
		}
	})
}

func TestComparacao(t *testing.T) {
	cem := mustParse(t, "100.00", "BRL")
	oitenta := mustParse(t, "80.00", "BRL")

	if got, _ := cem.Cmp(oitenta); got != 1 {
		t.Errorf("Cmp(100, 80) = %d, esperado 1", got)
	}
	if got, _ := oitenta.Cmp(cem); got != -1 {
		t.Errorf("Cmp(80, 100) = %d, esperado -1", got)
	}
	if got, _ := cem.Cmp(mustParse(t, "100.00", "BRL")); got != 0 {
		t.Errorf("Cmp(100, 100) = %d, esperado 0", got)
	}
	if !cem.Equal(mustParse(t, "100.0", "BRL")) {
		t.Error("Equal deveria enxergar \"100.00\" e \"100.0\" como o mesmo valor")
	}
}

func TestSerializacaoJSON(t *testing.T) {
	t.Run("marshal usa string decimal", func(t *testing.T) {
		got, err := json.Marshal(mustParse(t, "25.00", "BRL"))
		if err != nil {
			t.Fatalf("Marshal devolveu erro: %v", err)
		}
		want := `{"amount":"25.00","currency":"BRL"}`
		if string(got) != want {
			t.Errorf("Marshal = %s, esperado %s", got, want)
		}
	})

	t.Run("marshal é determinístico na ordem das chaves", func(t *testing.T) {
		// O hash de idempotência depende dessa estabilidade.
		m := mustParse(t, "25.00", "BRL")
		primeiro, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("Marshal devolveu erro: %v", err)
		}
		for i := 0; i < 50; i++ {
			atual, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("Marshal devolveu erro: %v", err)
			}
			if string(atual) != string(primeiro) {
				t.Fatalf("Marshal variou entre execuções: %s vs %s", primeiro, atual)
			}
		}
	})

	t.Run("marshal de valor não inicializado falha", func(t *testing.T) {
		var vazio money.Money
		if _, err := json.Marshal(vazio); !errors.Is(err, money.ErrUninitialized) {
			t.Errorf("Marshal devolveu %v, esperado ErrUninitialized", err)
		}
	})

	t.Run("unmarshal aceita o contrato externo", func(t *testing.T) {
		var m money.Money
		if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &m); err != nil {
			t.Fatalf("Unmarshal devolveu erro: %v", err)
		}
		if m.MinorUnits() != 2500 || m.Currency() != brl {
			t.Errorf("Unmarshal produziu %v/%v, esperado 2500/BRL", m.MinorUnits(), m.Currency())
		}
	})

	t.Run("unmarshal rejeita número JSON", func(t *testing.T) {
		// É por aqui que um float64 entraria no domínio sem ser notado.
		for _, corpo := range []string{
			`{"amount":25.00,"currency":"BRL"}`,
			`{"amount":25,"currency":"BRL"}`,
			`{"amount":2.5e1,"currency":"BRL"}`,
		} {
			var m money.Money
			if err := json.Unmarshal([]byte(corpo), &m); err == nil {
				t.Errorf("Unmarshal(%s) aceitou número, deveria exigir string", corpo)
			}
		}
	})

	t.Run("unmarshal propaga as regras de entrada", func(t *testing.T) {
		tests := []struct {
			corpo   string
			wantErr error
		}{
			{`{"amount":"-25.00","currency":"BRL"}`, money.ErrNegativeAmount},
			{`{"amount":"25.001","currency":"BRL"}`, money.ErrScaleExceeded},
			{`{"amount":"","currency":"BRL"}`, money.ErrInvalidAmount},
			{`{"amount":"1e5","currency":"BRL"}`, money.ErrInvalidAmount},
			{`{"amount":"25.00","currency":"brl"}`, money.ErrInvalidCurrency},
			{`{"amount":"25.00","currency":""}`, money.ErrInvalidCurrency},
		}
		for _, tt := range tests {
			var m money.Money
			err := json.Unmarshal([]byte(tt.corpo), &m)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Unmarshal(%s) devolveu %v, esperado %v", tt.corpo, err, tt.wantErr)
			}
		}
	})

	t.Run("unmarshal rejeita campos desconhecidos", func(t *testing.T) {
		var m money.Money
		if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL","scale":2}`), &m); err == nil {
			t.Error("Unmarshal aceitou campo desconhecido")
		}
	})

	t.Run("round-trip preserva valor e moeda", func(t *testing.T) {
		original := mustParse(t, "1000.07", "BRL")
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Marshal devolveu erro: %v", err)
		}
		var decoded money.Money
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("Unmarshal devolveu erro: %v", err)
		}
		if !decoded.Equal(original) {
			t.Errorf("round-trip produziu %v, esperado %v", decoded, original)
		}
	})
}

func TestPersistenciaPreservaValorExato(t *testing.T) {
	// Simula ida e volta ao BIGINT: nada pode ser perdido no caminho.
	for _, entrada := range []string{"0.00", "0.01", "25.00", "1000.07", "92233720368547758.07"} {
		original := mustParse(t, entrada, "BRL")
		reidratado, err := money.FromMinorUnits(original.MinorUnits(), original.Currency())
		if err != nil {
			t.Fatalf("FromMinorUnits devolveu erro: %v", err)
		}
		if !reidratado.Equal(original) {
			t.Errorf("reidratação de %q produziu %v", entrada, reidratado)
		}
	}
}
