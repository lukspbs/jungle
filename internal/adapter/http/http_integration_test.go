package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	adapterhttp "github.com/lukspbs/jungle/internal/adapter/http"
	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/test/authtest"
	"github.com/lukspbs/jungle/test/dbtest"
)

type api struct {
	servidor *httptest.Server
	prefixo  string

	// tokenPadrao acompanha toda requisição que não traga Authorization
	// própria. A autenticação é obrigatória, então um teste sem token é um
	// teste de 401 — e esses pedem o cabeçalho explicitamente.
	tokenPadrao string
}

// comToken devolve a mesma API falando por outra identidade.
func (a api) comToken(token string) api {
	a.tokenPadrao = token
	return a
}

type relogio struct{}

func (relogio) Now() time.Time { return time.Now().UTC() }

// loggerDeTeste descarta a saída: a suíte afirma sobre respostas, não sobre
// linhas de log, e o ruído atrapalharia a leitura das falhas.
func loggerDeTeste() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func novaAPI(t *testing.T) api {
	t.Helper()
	store := dbtest.Store(t)
	ids := app.UUIDv7{}
	clock := relogio{}

	politica := app.ReferencePolicy{
		TTL: time.Minute, MaxAttempts: 3,
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: time.Second,
	}

	handlers := adapterhttp.NewHandlers(
		app.NewOpenWallet(store, clock, ids),
		app.NewProcessWager(store, clock, ids, politica),
		app.NewQueries(store),
		app.NewReadiness(store),
	)
	servidor := httptest.NewServer(adapterhttp.NewRouter(handlers, authtest.Verifier(t), loggerDeTeste()))
	t.Cleanup(servidor.Close)

	return api{
		servidor:    servidor,
		prefixo:     uuid.NewString(),
		tokenPadrao: authtest.Internal(t),
	}
}

func (a api) do(t *testing.T, metodo, caminho string, corpo any, headers map[string]string) (int, []byte) {
	t.Helper()
	var leitor *bytes.Reader
	if corpo != nil {
		raw, err := json.Marshal(corpo)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		leitor = bytes.NewReader(raw)
	} else {
		leitor = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(context.Background(), metodo, a.servidor.URL+caminho, leitor)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	a.autentica(req, headers)

	res, err := a.servidor.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer res.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	return res.StatusCode, buf.Bytes()
}

func (a api) doRaw(t *testing.T, metodo, caminho, corpo string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), metodo,
		a.servidor.URL+caminho, bytes.NewReader([]byte(corpo)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	a.autentica(req, headers)

	res, err := a.servidor.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}

// autentica aplica os cabeçalhos pedidos e, se nenhum trouxe Authorization,
// anexa o token padrão da identidade atual.
func (a api) autentica(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Authorization") == "" && a.tokenPadrao != "" {
		req.Header.Set("Authorization", authtest.Bearer(a.tokenPadrao))
	}
}

func decodificar(t *testing.T, raw []byte, destino any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	// UseNumber mantém números como json.Number em vez de convertê-los para
	// ponto flutuante. Os números deste contrato são inteiros — versão da
	// carteira, contagem de lançamentos — e não têm por que passar por float.
	dec.UseNumber()
	if err := dec.Decode(destino); err != nil {
		t.Fatalf("resposta não é JSON válido (%s): %v", raw, err)
	}
}

// inteiroJSON extrai um inteiro de um valor desserializado.
func inteiroJSON(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%v não é número JSON (%T)", v, v)
	}
	i, err := n.Int64()
	if err != nil {
		t.Fatalf("%v não é inteiro: %v", v, err)
	}
	return i
}

// abreCarteira cria uma carteira pela API e devolve o corpo da resposta.
func (a api) abreCarteira(t *testing.T, saldo string) map[string]any {
	t.Helper()
	status, corpo := a.do(t, "POST", "/wallets", map[string]any{
		"playerId":       uuid.NewString(),
		"initialBalance": map[string]string{"amount": saldo, "currency": "BRL"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("abertura devolveu %d: %s", status, corpo)
	}
	var out map[string]any
	decodificar(t, corpo, &out)
	return out
}

func (a api) aposta(walletID, playerID, kind, valor, externalID string) (string, map[string]any) {
	id := a.prefixo + "-" + externalID
	return "provider-a:" + id, map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": id,
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
		"kind":                  kind,
		"money":                 map[string]string{"amount": valor, "currency": "BRL"},
	}
}

func TestAberturaDeCarteiraPelaAPI(t *testing.T) {
	a := novaAPI(t)
	playerID := uuid.NewString()

	status, corpo := a.do(t, "POST", "/wallets", map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": "1000.00", "currency": "BRL"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("status = %d: %s", status, corpo)
	}

	var res map[string]any
	decodificar(t, corpo, &res)
	if res["playerId"] != playerID {
		t.Errorf("playerId = %v", res["playerId"])
	}
	if v := inteiroJSON(t, res["version"]); v != 1 {
		t.Errorf("version = %d, esperado 1", v)
	}
	saldo := res["balance"].(map[string]any)
	if saldo["amount"] != "1000.00" || saldo["currency"] != "BRL" {
		t.Errorf("balance = %v", saldo)
	}

	t.Run("segunda abertura é conflito", func(t *testing.T) {
		status, corpo := a.do(t, "POST", "/wallets", map[string]any{
			"playerId":       playerID,
			"initialBalance": map[string]string{"amount": "10.00", "currency": "BRL"},
		}, nil)
		if status != http.StatusConflict {
			t.Errorf("status = %d, esperado 409: %s", status, corpo)
		}
	})
}

func TestEntradaInvalidaNaAbertura(t *testing.T) {
	a := novaAPI(t)

	tests := []struct {
		name  string
		corpo string
	}{
		{"playerId malformado", `{"playerId":"nao-e-uuid","initialBalance":{"amount":"10.00","currency":"BRL"}}`},
		{"valor negativo", `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"-10.00","currency":"BRL"}}`},
		{"escala excedida", `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"10.001","currency":"BRL"}}`},
		{"valor como número", `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":10.00,"currency":"BRL"}}`},
		{"notação científica", `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"1e3","currency":"BRL"}}`},
		{"moeda inválida", `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"10.00","currency":"brl"}}`},
		{"campo desconhecido", `{"playerId":"` + uuid.NewString() + `","initialBalance":{"amount":"10.00","currency":"BRL"},"extra":1}`},
		{"json malformado", `{"playerId":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, corpo := a.doRaw(t, "POST", "/wallets", tt.corpo, nil)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, esperado 400: %s", status, corpo)
			}
			var p map[string]any
			decodificar(t, corpo, &p)
			if p["code"] != "INVALID_REQUEST" {
				t.Errorf("code = %v", p["code"])
			}
			if p["correlationId"] == "" || p["correlationId"] == nil {
				t.Error("resposta de erro sem correlationId")
			}
		})
	}
}

func TestOperacaoPelaAPI(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "1000.00")
	a := interno.comToken(authtest.ProviderA(t))
	walletID := carteira["id"].(string)
	playerID := carteira["playerId"].(string)

	chave, corpo := a.aposta(walletID, playerID, "BET", "25.00", "bet-1")

	t.Run("sem Idempotency-Key é 400", func(t *testing.T) {
		status, raw := a.do(t, "POST", "/wagering/transactions", corpo, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, esperado 400: %s", status, raw)
		}
		var p map[string]any
		decodificar(t, raw, &p)
		if p["code"] != "MISSING_IDEMPOTENCY_KEY" {
			t.Errorf("code = %v", p["code"])
		}
	})

	t.Run("aposta processada é 200", func(t *testing.T) {
		status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
			map[string]string{"Idempotency-Key": chave})
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, raw)
		}
		var res map[string]any
		decodificar(t, raw, &res)
		if res["status"] != "PROCESSED" {
			t.Errorf("status = %v", res["status"])
		}
		if res["idempotentReplay"] != false {
			t.Errorf("idempotentReplay = %v, esperado false", res["idempotentReplay"])
		}
		saldo := res["balance"].(map[string]any)
		if saldo["amount"] != "975.00" {
			t.Errorf("balance = %v, esperado 975.00", saldo)
		}
	})

	t.Run("reenvio é replay com o mesmo 200", func(t *testing.T) {
		status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
			map[string]string{"Idempotency-Key": chave})
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, raw)
		}
		var res map[string]any
		decodificar(t, raw, &res)
		if res["idempotentReplay"] != true {
			t.Errorf("idempotentReplay = %v, esperado true", res["idempotentReplay"])
		}
	})

	t.Run("mesma chave com outro conteúdo é 409", func(t *testing.T) {
		outro := map[string]any{}
		for k, v := range corpo {
			outro[k] = v
		}
		outro["money"] = map[string]string{"amount": "99.00", "currency": "BRL"}

		status, raw := a.do(t, "POST", "/wagering/transactions", outro,
			map[string]string{"Idempotency-Key": chave})
		if status != http.StatusConflict {
			t.Fatalf("status = %d, esperado 409: %s", status, raw)
		}
		var p map[string]any
		decodificar(t, raw, &p)
		if p["code"] != "IDEMPOTENCY_CONFLICT" {
			t.Errorf("code = %v", p["code"])
		}
	})
}

func TestRecusaDeNegocioEh422(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "10.00")
	a := interno.comToken(authtest.ProviderA(t))
	chave, corpo := a.aposta(carteira["id"].(string), carteira["playerId"].(string),
		"BET", "500.00", "sem-saldo")

	status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
		map[string]string{"Idempotency-Key": chave})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, esperado 422: %s", status, raw)
	}

	var res map[string]any
	decodificar(t, raw, &res)
	if res["status"] != "REJECTED" {
		t.Errorf("status = %v", res["status"])
	}
	if res["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Errorf("failureCode = %v", res["failureCode"])
	}
	if res["correctable"] != false {
		t.Errorf("correctable = %v, esperado false", res["correctable"])
	}
	if _, tem := res["balance"]; tem {
		t.Error("recusa trouxe saldo, que não existe nesse desfecho")
	}
}

func TestPendenciaDeReferenciaEh202(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "1000.00")
	a := interno.comToken(authtest.ProviderA(t))
	chave, corpo := a.aposta(carteira["id"].(string), carteira["playerId"].(string),
		"REFUND", "25.00", "estorno-orfao")
	corpo["referenceExternalTransactionId"] = a.prefixo + "-nunca-chegou"

	status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
		map[string]string{"Idempotency-Key": chave})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, esperado 202: %s", status, raw)
	}
	var res map[string]any
	decodificar(t, raw, &res)
	if res["status"] != "PENDING_REFERENCE" {
		t.Errorf("status = %v", res["status"])
	}
}

func TestOpeningPelaAPIEhRecusado(t *testing.T) {
	interno := novaAPI(t)
	carteira := interno.abreCarteira(t, "100.00")
	a := interno.comToken(authtest.ProviderA(t))
	chave, corpo := a.aposta(carteira["id"].(string), carteira["playerId"].(string),
		"OPENING", "500.00", "abertura-externa")

	status, raw := a.do(t, "POST", "/wagering/transactions", corpo,
		map[string]string{"Idempotency-Key": chave})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, esperado 400: %s", status, raw)
	}
}

func TestConsultas(t *testing.T) {
	a := novaAPI(t)
	carteira := a.abreCarteira(t, "1000.00")
	walletID := carteira["id"].(string)
	playerID := carteira["playerId"].(string)
	provedor := a.comToken(authtest.ProviderA(t))

	for i := 0; i < 5; i++ {
		chave, corpo := a.aposta(walletID, playerID, "BET", "10.00", fmt.Sprintf("consulta-%d", i))
		if status, raw := provedor.do(t, "POST", "/wagering/transactions", corpo,
			map[string]string{"Idempotency-Key": chave}); status != http.StatusOK {
			t.Fatalf("aposta %d: %d %s", i, status, raw)
		}
	}

	t.Run("carteira", func(t *testing.T) {
		status, raw := a.do(t, "GET", "/wallets/"+walletID, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, raw)
		}
		var res map[string]any
		decodificar(t, raw, &res)
		if res["balance"].(map[string]any)["amount"] != "950.00" {
			t.Errorf("balance = %v", res["balance"])
		}
	})

	t.Run("carteira inexistente é 404", func(t *testing.T) {
		status, _ := a.do(t, "GET", "/wallets/"+uuid.NewString(), nil, nil)
		if status != http.StatusNotFound {
			t.Errorf("status = %d, esperado 404", status)
		}
	})

	t.Run("id malformado é 400", func(t *testing.T) {
		status, _ := a.do(t, "GET", "/wallets/nao-e-uuid", nil, nil)
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, esperado 400", status)
		}
	})

	t.Run("extrato pagina com cursor opaco", func(t *testing.T) {
		vistos := map[string]bool{}
		caminho := "/wallets/" + walletID + "/ledger?limit=2"
		for paginas := 0; paginas < 10; paginas++ {
			status, raw := a.do(t, "GET", caminho, nil, nil)
			if status != http.StatusOK {
				t.Fatalf("status = %d: %s", status, raw)
			}
			var res struct {
				Entries []struct {
					ID string `json:"id"`
				} `json:"entries"`
				NextCursor string `json:"nextCursor"`
			}
			decodificar(t, raw, &res)
			for _, e := range res.Entries {
				if vistos[e.ID] {
					t.Fatalf("lançamento %s repetiu entre páginas", e.ID)
				}
				vistos[e.ID] = true
			}
			if res.NextCursor == "" {
				break
			}
			// O cursor é opaco: nada no teste interpreta o conteúdo dele.
			caminho = "/wallets/" + walletID + "/ledger?limit=2&cursor=" + res.NextCursor
		}
		if len(vistos) != 6 {
			t.Errorf("lançamentos = %d, esperado 6 (abertura + 5 apostas)", len(vistos))
		}
	})

	t.Run("cursor inválido é 400", func(t *testing.T) {
		status, _ := a.do(t, "GET", "/wallets/"+walletID+"/ledger?cursor=lixo!!!", nil, nil)
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, esperado 400", status)
		}
	})

	t.Run("transação por provedor e id externo", func(t *testing.T) {
		externalID := a.prefixo + "-consulta-0"
		status, raw := provedor.do(t, "GET",
			"/providers/provider-a/wagering/transactions/"+externalID, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, raw)
		}
		var res map[string]any
		decodificar(t, raw, &res)
		if res["kind"] != "BET" || res["status"] != "PROCESSED" {
			t.Errorf("transação = %v/%v", res["kind"], res["status"])
		}

		t.Run("outro provedor não enxerga", func(t *testing.T) {
			outro := a.comToken(authtest.ProviderB(t))
			status, _ := outro.do(t, "GET",
				"/providers/provider-a/wagering/transactions/"+externalID, nil, nil)
			if status != http.StatusNotFound {
				t.Errorf("status = %d, esperado 404: o provider-b enxergou a operação do provider-a", status)
			}
		})
	})

	t.Run("reconciliação", func(t *testing.T) {
		status, raw := a.do(t, "POST", "/wallets/"+walletID+"/reconciliation", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %s", status, raw)
		}
		var res map[string]any
		decodificar(t, raw, &res)
		if res["consistent"] != true {
			t.Errorf("consistent = %v: %s", res["consistent"], raw)
		}
		if res["difference"].(map[string]any)["amount"] != "0.00" {
			t.Errorf("difference = %v", res["difference"])
		}
		if n := inteiroJSON(t, res["checkedEntries"]); n != 6 {
			t.Errorf("checkedEntries = %d, esperado 6", n)
		}
	})
}

func TestHealthChecks(t *testing.T) {
	a := novaAPI(t)

	for _, caminho := range []string{"/health/live", "/health/ready"} {
		status, raw := a.do(t, "GET", caminho, nil, nil)
		if status != http.StatusOK {
			t.Errorf("%s devolveu %d: %s", caminho, status, raw)
		}
	}
}

func TestCorrelationIdEhEcoadoOuGerado(t *testing.T) {
	a := novaAPI(t)

	req, _ := http.NewRequest("GET", a.servidor.URL+"/health/live", nil)
	req.Header.Set("X-Correlation-Id", "meu-id-123")
	res, err := a.servidor.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Correlation-Id"); got != "meu-id-123" {
		t.Errorf("correlationId = %q, esperado o informado", got)
	}

	res, err = a.servidor.Client().Get(a.servidor.URL + "/health/live")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	res.Body.Close()
	if res.Header.Get("X-Correlation-Id") == "" {
		t.Error("o servidor não gerou correlationId quando o cliente não informou")
	}
}
