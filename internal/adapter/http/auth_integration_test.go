package http_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/test/authtest"
)

// TestSemCredencialNadaPassa confere que todo endpoint de negócio exige token.
func TestSemCredencialNadaPassa(t *testing.T) {
	a := novaAPI(t).comToken("")
	id := uuid.NewString()

	rotas := []struct {
		metodo  string
		caminho string
	}{
		{"POST", "/wallets"},
		{"GET", "/wallets/" + id},
		{"GET", "/wallets/" + id + "/ledger"},
		{"POST", "/wallets/" + id + "/reconciliation"},
		{"POST", "/wagering/transactions"},
		{"GET", "/wagering/transactions/" + id},
		{"GET", "/providers/provider-a/wagering/transactions/ext-1"},
	}

	for _, rota := range rotas {
		t.Run(rota.metodo+" "+rota.caminho, func(t *testing.T) {
			status, corpo := a.do(t, rota.metodo, rota.caminho, map[string]any{}, nil)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, esperado 401: %s", status, corpo)
			}
			var p map[string]any
			decodificar(t, corpo, &p)
			if p["code"] != "UNAUTHENTICATED" {
				t.Errorf("code = %v", p["code"])
			}
		})
	}
}

func TestCredencialInvalidaEhRecusada(t *testing.T) {
	a := novaAPI(t)

	tests := []struct {
		nome   string
		header string
	}{
		{"token aleatório", "Bearer nao-e-um-jwt"},
		{"jwt malformado", "Bearer aaa.bbb.ccc"},
		{"esquema errado", "Basic dXNlcjpwYXNz"},
		{"sem esquema", authtest.Internal(t)},
	}

	for _, tt := range tests {
		t.Run(tt.nome, func(t *testing.T) {
			status, corpo := a.do(t, "GET", "/wallets/"+uuid.NewString(), nil,
				map[string]string{"Authorization": tt.header})
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, esperado 401: %s", status, corpo)
			}
		})
	}
}

func TestHealthChecksSaoPublicos(t *testing.T) {
	a := novaAPI(t).comToken("")

	for _, caminho := range []string{"/health/live", "/health/ready"} {
		status, corpo := a.do(t, "GET", caminho, nil, nil)
		if status != http.StatusOK {
			t.Errorf("%s sem credencial devolveu %d: %s", caminho, status, corpo)
		}
	}
}

// TestOperacoesDeCarteiraSaoDoServicoInterno cobre a exigência do §2: um
// provedor não abre nem consulta carteira.
func TestOperacoesDeCarteiraSaoDoServicoInterno(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "100.00")
	walletID := carteira["id"].(string)

	provedor := interno.comToken(authtest.ProviderA(t))

	rotas := []struct {
		metodo  string
		caminho string
		corpo   any
	}{
		{"POST", "/wallets", map[string]any{
			"playerId":       uuid.NewString(),
			"initialBalance": map[string]string{"amount": "10.00", "currency": "BRL"},
		}},
		{"GET", "/wallets/" + walletID, nil},
		{"GET", "/wallets/" + walletID + "/ledger", nil},
		{"POST", "/wallets/" + walletID + "/reconciliation", nil},
	}

	for _, rota := range rotas {
		t.Run(rota.metodo+" "+rota.caminho, func(t *testing.T) {
			status, corpo := provedor.do(t, rota.metodo, rota.caminho, rota.corpo, nil)
			if status != http.StatusForbidden {
				t.Errorf("status = %d, esperado 403: %s", status, corpo)
			}
			var p map[string]any
			decodificar(t, corpo, &p)
			if p["code"] != "FORBIDDEN" {
				t.Errorf("code = %v", p["code"])
			}
		})
	}
}

// TestServicoInternoNaoEnviaOperacaoDeProvedor é o outro lado da separação.
func TestServicoInternoNaoEnviaOperacaoDeProvedor(t *testing.T) {
	a := novaAPI(t)
	carteira := a.abreCarteira(t, "100.00")
	chave, corpo := a.aposta(carteira["id"].(string), carteira["playerId"].(string),
		"BET", "10.00", "interno-aposta")

	status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
		map[string]string{"Idempotency-Key": chave})
	if status != http.StatusForbidden {
		t.Errorf("status = %d, esperado 403: %s", status, raw)
	}
}

// TestProvedorNaoAgePorOutro cobre a regra de que a identidade autenticada
// determina o providerId, e não o corpo da requisição.
func TestProvedorNaoAgePorOutro(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "1000.00")
	walletID := carteira["id"].(string)
	playerID := carteira["playerId"].(string)

	a := interno.comToken(authtest.ProviderA(t))
	chave, corpo := a.aposta(walletID, playerID, "BET", "25.00", "finge-ser-b")
	corpo["providerId"] = "provider-b"

	status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
		map[string]string{"Idempotency-Key": chave})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, esperado 403: %s", status, raw)
	}

	// E o acesso recusado não deixou efeito financeiro.
	status, raw = interno.do(t, "GET", "/wallets/"+walletID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("consulta da carteira: %d %s", status, raw)
	}
	var w map[string]any
	decodificar(t, raw, &w)
	if saldo := w["balance"].(map[string]any)["amount"]; saldo != "1000.00" {
		t.Errorf("saldo = %v: a tentativa recusada movimentou a carteira", saldo)
	}
}

// TestIsolamentoEntreProvedores cobre consultas e replays, conforme o §2.
func TestIsolamentoEntreProvedores(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "1000.00")
	walletID := carteira["id"].(string)
	playerID := carteira["playerId"].(string)

	a := interno.comToken(authtest.ProviderA(t))
	b := interno.comToken(authtest.ProviderB(t))

	chave, corpo := a.aposta(walletID, playerID, "BET", "25.00", "isolada")
	status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
		map[string]string{"Idempotency-Key": chave})
	if status != http.StatusOK {
		t.Fatalf("aposta do provider-a: %d %s", status, raw)
	}
	var criada map[string]any
	decodificar(t, raw, &criada)
	transactionID := criada["transactionId"].(string)
	externalID := a.prefixo + "-isolada"

	t.Run("o dono consulta pelo id interno", func(t *testing.T) {
		if status, raw := a.do(t, "GET", "/wagering/transactions/"+transactionID, nil, nil); status != http.StatusOK {
			t.Errorf("status = %d: %s", status, raw)
		}
	})

	t.Run("outro provedor recebe 404 pelo id interno", func(t *testing.T) {
		status, _ := b.do(t, "GET", "/wagering/transactions/"+transactionID, nil, nil)
		if status != http.StatusNotFound {
			t.Errorf("status = %d, esperado 404", status)
		}
	})

	t.Run("outro provedor recebe 404 pelo id externo", func(t *testing.T) {
		status, _ := b.do(t, "GET",
			"/providers/provider-a/wagering/transactions/"+externalID, nil, nil)
		if status != http.StatusNotFound {
			t.Errorf("status = %d, esperado 404", status)
		}
	})

	t.Run("o replay também é isolado", func(t *testing.T) {
		// O provider-b reenvia a mesma operação. Como a busca de replay é
		// escopada pelo provedor do token, ele não encontra a do provider-a —
		// e a tentativa de registrar a sua é recusada pelo providerId do corpo.
		corpoB := map[string]any{}
		for k, v := range corpo {
			corpoB[k] = v
		}
		status, raw := b.do(t, "POST", "/wagering/transactions", corpoB,
			map[string]string{"Idempotency-Key": chave})
		if status != http.StatusForbidden {
			t.Errorf("status = %d, esperado 403: %s", status, raw)
		}
	})

	t.Run("o serviço interno enxerga qualquer provedor", func(t *testing.T) {
		if status, raw := interno.do(t, "GET",
			"/providers/provider-a/wagering/transactions/"+externalID, nil, nil); status != http.StatusOK {
			t.Errorf("status = %d: %s", status, raw)
		}
	})

	t.Run("o saldo não mudou com as tentativas recusadas", func(t *testing.T) {
		status, raw := interno.do(t, "GET", "/wallets/"+walletID, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		var w map[string]any
		decodificar(t, raw, &w)
		if saldo := w["balance"].(map[string]any)["amount"]; saldo != "975.00" {
			t.Errorf("saldo = %v, esperado \"975.00\"", saldo)
		}
	})
}
