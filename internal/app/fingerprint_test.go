package app_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/money"
	"github.com/lukspbs/jungle/internal/domain/wagering"
)

var (
	jogador  = uuid.MustParse("0192f28f-0000-7000-8000-0000000000a1")
	carteira = uuid.MustParse("0192f291-0000-7000-8000-000000000001")
)

func camposBase(t *testing.T) app.FingerprintFields {
	t.Helper()
	return app.FingerprintFields{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		PlayerID:              jogador,
		WalletID:              carteira,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  wagering.Bet,
		Money:                 brlOf(t, "25.00"),
	}
}

func hashDe(t *testing.T, f app.FingerprintFields) app.Fingerprint {
	t.Helper()
	fp, err := app.ComputeFingerprint(f)
	if err != nil {
		t.Fatalf("ComputeFingerprint: %v", err)
	}
	return fp
}

func TestHashEhDeterministico(t *testing.T) {
	primeiro := hashDe(t, camposBase(t))
	for i := 0; i < 100; i++ {
		if hashDe(t, camposBase(t)) != primeiro {
			t.Fatal("o hash variou entre execuções com os mesmos campos")
		}
	}
	if len(primeiro.String()) != 64 {
		t.Errorf("hexadecimal com %d caracteres, esperado 64", len(primeiro.String()))
	}
}

// TestFormasEquivalentesDeValorNaoGeramConflito é o ponto do desafio: se o
// provedor mandar "25" numa vez e "25.00" na outra, isso é replay e não
// conflito de payload.
func TestFormasEquivalentesDeValorNaoGeramConflito(t *testing.T) {
	esperado := hashDe(t, camposBase(t))

	for _, forma := range []string{"25", "25.0", "25.00", "0025.00"} {
		f := camposBase(t)
		f.Money = brlOf(t, forma)
		if hashDe(t, f) != esperado {
			t.Errorf("%q produziu hash diferente de \"25.00\"", forma)
		}
	}
}

// TestChaveDeIdempotenciaNaoEntraNoHash garante que a mesma operação enviada
// sob chaves diferentes seja reconhecida como a mesma operação.
func TestChaveDeIdempotenciaNaoEntraNoHash(t *testing.T) {
	// FingerprintFields sequer tem campo para a chave: a exclusão é estrutural,
	// não uma omissão que alguém possa reverter por engano. O teste afirma o
	// contrato que decorre disso — campos de negócio iguais, hash igual.
	a := hashDe(t, camposBase(t))
	b := hashDe(t, camposBase(t))
	if a != b {
		t.Error("campos de negócio iguais produziram hashes diferentes")
	}
}

func TestQualquerCampoDeNegocioMudaOHash(t *testing.T) {
	base := hashDe(t, camposBase(t))

	tests := []struct {
		name   string
		mutate func(*app.FingerprintFields)
	}{
		{"provedor", func(f *app.FingerprintFields) { f.ProviderID = "provider-b" }},
		{"id externo", func(f *app.FingerprintFields) { f.ExternalTransactionID = "transaction-124" }},
		{"jogador", func(f *app.FingerprintFields) { f.PlayerID = uuid.New() }},
		{"carteira", func(f *app.FingerprintFields) { f.WalletID = uuid.New() }},
		{"rodada", func(f *app.FingerprintFields) { f.RoundID = "round-988" }},
		{"jogo", func(f *app.FingerprintFields) { f.GameID = "outro-jogo" }},
		{"tipo", func(f *app.FingerprintFields) { f.Kind = wagering.Win }},
		{"valor", func(f *app.FingerprintFields) { f.Money = brlOf(t, "25.01") }},
		{"referência", func(f *app.FingerprintFields) { f.ReferenceExternalTransactionID = "transaction-000" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := camposBase(t)
			tt.mutate(&f)
			if hashDe(t, f) == base {
				t.Errorf("mudar %s não alterou o hash", tt.name)
			}
		})
	}
}

// TestMoedaDiferenteMudaOHash cobre o caso em que o número é o mesmo mas o
// dinheiro não.
func TestMoedaDiferenteMudaOHash(t *testing.T) {
	emDolar, err := money.Parse("25.00", "USD")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := camposBase(t)
	f.Money = emDolar

	if hashDe(t, f) == hashDe(t, camposBase(t)) {
		t.Error("25.00 BRL e 25.00 USD produziram o mesmo hash")
	}
}

func TestHashExigeMoeda(t *testing.T) {
	f := camposBase(t)
	f.Money = money.Money{}
	if _, err := app.ComputeFingerprint(f); !errors.Is(err, app.ErrInvalidCommand) {
		t.Errorf("devolveu %v, esperado ErrInvalidCommand", err)
	}
}

func TestComparacaoComOHashPersistido(t *testing.T) {
	fp := hashDe(t, camposBase(t))

	if !fp.Equal(fp.Bytes()) {
		t.Error("o hash não reconheceu os próprios bytes")
	}
	outro := camposBase(t)
	outro.Money = brlOf(t, "26.00")
	if fp.Equal(hashDe(t, outro).Bytes()) {
		t.Error("hashes de payloads diferentes foram considerados iguais")
	}
	if fp.Equal([]byte{0x01, 0x02}) {
		t.Error("hash de tamanho diferente foi considerado igual")
	}
	if fp.Equal(nil) {
		t.Error("hash nulo foi considerado igual")
	}
}

// TestEquivalenciaEntreHttpESqs demonstra o requisito de que as duas entradas
// cheguem ao mesmo hash. Elas chegam porque nenhuma das duas o calcula: os
// adaptadores montam os campos de negócio e a camada de aplicação é quem
// hasheia, num ponto único.
func TestEquivalenciaEntreHttpESqs(t *testing.T) {
	// O corpo HTTP traz "25.00" e cabeçalho Idempotency-Key.
	viaHTTP := camposBase(t)

	// A mensagem SQS traz "25" no mesmo campo, mais messageId e occurredAt no
	// envelope — metadados de transporte que não entram no cálculo.
	viaSQS := camposBase(t)
	viaSQS.Money = brlOf(t, "25")

	if hashDe(t, viaHTTP) != hashDe(t, viaSQS) {
		t.Error("a mesma operação por HTTP e por SQS produziu hashes diferentes")
	}
}
