// Package scenarios exercita as garantias entre processos independentes.
//
// Diferente dos demais testes de integração, estes falam com instâncias reais
// pela rede: três processos separados, cada um com o próprio pool de conexões e
// a própria memória, coordenando-se apenas pelo banco. É a forma de demonstrar
// que nenhuma garantia depende de estado compartilhado dentro de um processo.
//
// As instâncias sobem com:
//
//	docker compose --profile multi up -d
//
// e os endereços chegam por TEST_API_URLS. Sem a variável, o pacote é pulado.
package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/test/authtest"
)

// EnvAPIs lista as instâncias, separadas por vírgula.
const EnvAPIs = "TEST_API_URLS"

type cluster struct {
	instancias []string
	interno    string
	provedor   string
	prefixo    string
	cliente    *http.Client
}

func novoCluster(t *testing.T) cluster {
	t.Helper()

	bruto := os.Getenv(EnvAPIs)
	if bruto == "" {
		t.Skipf("%s não definida: cenário de múltiplas instâncias pulado", EnvAPIs)
	}
	instancias := strings.Split(bruto, ",")
	if len(instancias) < 3 {
		t.Skipf("%s traz %d instância(s); o cenário exige ao menos três",
			EnvAPIs, len(instancias))
	}
	for i := range instancias {
		instancias[i] = strings.TrimSpace(strings.TrimSuffix(instancias[i], "/"))
	}

	c := cluster{
		instancias: instancias,
		interno:    authtest.Internal(t),
		provedor:   authtest.ProviderA(t),
		prefixo:    uuid.NewString(),
		cliente:    &http.Client{Timeout: 20 * time.Second},
	}
	c.exigeTodasVivas(t)
	return c
}

// exigeTodasVivas confirma que as três respondem antes de afirmar qualquer
// coisa sobre concorrência entre elas.
func (c cluster) exigeTodasVivas(t *testing.T) {
	t.Helper()
	for _, url := range c.instancias {
		res, err := c.cliente.Get(url + "/health/ready")
		if err != nil {
			t.Skipf("instância %s não respondeu: %v", url, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Skipf("instância %s não está pronta: HTTP %d", url, res.StatusCode)
		}
	}
}

func (c cluster) do(t *testing.T, instancia int, metodo, caminho, token string,
	corpo any, headers map[string]string,
) (int, []byte) {
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

	url := c.instancias[instancia%len(c.instancias)]
	req, err := http.NewRequestWithContext(context.Background(), metodo, url+caminho, leitor)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authtest.Bearer(token))
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := c.cliente.Do(req)
	if err != nil {
		t.Fatalf("%s %s na instância %s: %v", metodo, caminho, url, err)
	}
	defer res.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	return res.StatusCode, buf.Bytes()
}

func (c cluster) abreCarteira(t *testing.T, saldo string) (walletID, playerID string) {
	t.Helper()
	playerID = uuid.NewString()

	status, corpo := c.do(t, 0, "POST", "/wallets", c.interno, map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": saldo, "currency": "BRL"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("abertura devolveu %d: %s", status, corpo)
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(corpo, &res); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	return res.ID, playerID
}

func (c cluster) aposta(walletID, playerID, externalID, valor string) (string, map[string]any) {
	id := c.prefixo + "-" + externalID
	return "provider-a:" + id, map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": id,
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": valor, "currency": "BRL"},
	}
}

func (c cluster) saldo(t *testing.T, walletID string) string {
	t.Helper()
	status, corpo := c.do(t, 0, "GET", "/wallets/"+walletID, c.interno, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("consulta da carteira: %d %s", status, corpo)
	}
	var res struct {
		Balance struct {
			Amount string `json:"amount"`
		} `json:"balance"`
	}
	if err := json.Unmarshal(corpo, &res); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	return res.Balance.Amount
}

// reconcilia confere que o saldo armazenado bate com o reconstruído pelo
// ledger. É a verificação final exigida pelo desafio.
func (c cluster) reconcilia(t *testing.T, walletID string) (consistente bool, entradas int) {
	t.Helper()
	status, corpo := c.do(t, 0, "POST", "/wallets/"+walletID+"/reconciliation", c.interno, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("reconciliação: %d %s", status, corpo)
	}
	var res struct {
		Consistent     bool `json:"consistent"`
		CheckedEntries int  `json:"checkedEntries"`
	}
	if err := json.Unmarshal(corpo, &res); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	return res.Consistent, res.CheckedEntries
}

// TestMesmaApostaEmTresInstancias é o cenário 1 do desafio, agora distribuído:
// cinquenta envios da mesma operação, espalhados pelas três instâncias.
func TestMesmaApostaEmTresInstancias(t *testing.T) {
	c := novoCluster(t)
	walletID, playerID := c.abreCarteira(t, "1000.00")
	chave, corpo := c.aposta(walletID, playerID, "multi-50x", "25.00")

	const envios = 50
	type resposta struct {
		status int
		replay bool
		txID   string
	}
	respostas := make([]resposta, envios)

	var wg sync.WaitGroup
	largada := make(chan struct{})
	for i := 0; i < envios; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-largada
			status, raw := c.do(t, i, "POST", "/wagering/transactions", c.provedor,
				corpo, map[string]string{"Idempotency-Key": chave})

			var res struct {
				TransactionID    string `json:"transactionId"`
				IdempotentReplay bool   `json:"idempotentReplay"`
			}
			_ = json.Unmarshal(raw, &res)
			respostas[i] = resposta{status: status, replay: res.IdempotentReplay, txID: res.TransactionID}
		}(i)
	}
	close(largada)
	wg.Wait()

	var primeiras int
	transacoes := map[string]bool{}
	for i, r := range respostas {
		if r.status != http.StatusOK {
			t.Fatalf("envio %d devolveu %d", i, r.status)
		}
		if !r.replay {
			primeiras++
		}
		transacoes[r.txID] = true
	}

	if primeiras != 1 {
		t.Errorf("processamentos reais = %d, esperado 1", primeiras)
	}
	if len(transacoes) != 1 {
		t.Errorf("transações distintas = %d, esperado 1", len(transacoes))
	}
	if got := c.saldo(t, walletID); got != "975.00" {
		t.Errorf("saldo = %q, esperado \"975.00\": houve movimentação duplicada", got)
	}

	consistente, entradas := c.reconcilia(t, walletID)
	if !consistente {
		t.Error("o saldo armazenado divergiu do reconstruído pelo ledger")
	}
	if entradas != 2 {
		t.Errorf("lançamentos = %d, esperado 2 (abertura + um débito)", entradas)
	}
}

// TestDuasApostasDe80EmInstanciasDistintas é o cenário 2 do desafio, com as
// duas apostas chegando a processos diferentes ao mesmo tempo.
func TestDuasApostasDe80EmInstanciasDistintas(t *testing.T) {
	c := novoCluster(t)
	walletID, playerID := c.abreCarteira(t, "100.00")

	type resposta struct {
		status      int
		estado      string
		failureCode string
	}
	respostas := make([]resposta, 2)

	var wg sync.WaitGroup
	largada := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chave, corpo := c.aposta(walletID, playerID, fmt.Sprintf("multi-disputa-%d", i), "80.00")
			<-largada
			// Instâncias diferentes: i=0 vai para a primeira, i=1 para a segunda.
			status, raw := c.do(t, i, "POST", "/wagering/transactions", c.provedor,
				corpo, map[string]string{"Idempotency-Key": chave})

			var res struct {
				Status      string `json:"status"`
				FailureCode string `json:"failureCode"`
			}
			_ = json.Unmarshal(raw, &res)
			respostas[i] = resposta{status: status, estado: res.Status, failureCode: res.FailureCode}
		}(i)
	}
	close(largada)
	wg.Wait()

	var processadas, recusadas int
	for i, r := range respostas {
		switch r.estado {
		case "PROCESSED":
			processadas++
			if r.status != http.StatusOK {
				t.Errorf("aposta %d processada com HTTP %d", i, r.status)
			}
		case "REJECTED":
			recusadas++
			if r.status != http.StatusUnprocessableEntity {
				t.Errorf("aposta %d recusada com HTTP %d, esperado 422", i, r.status)
			}
			if r.failureCode != "INSUFFICIENT_FUNDS" {
				t.Errorf("aposta %d recusada com código %q", i, r.failureCode)
			}
		default:
			t.Fatalf("aposta %d terminou em %q (HTTP %d)", i, r.estado, r.status)
		}
	}

	if processadas != 1 || recusadas != 1 {
		t.Fatalf("processadas=%d recusadas=%d, esperado 1 e 1", processadas, recusadas)
	}
	if got := c.saldo(t, walletID); got != "20.00" {
		t.Errorf("saldo final = %q, esperado \"20.00\"", got)
	}

	consistente, entradas := c.reconcilia(t, walletID)
	if !consistente {
		t.Error("divergência entre o saldo e o ledger")
	}
	if entradas != 2 {
		t.Errorf("lançamentos = %d, esperado 2: só um débito deveria existir", entradas)
	}
}

// TestCarteirasDistintasEmParaleloEntreInstancias confere que o lock é por
// carteira também entre processos: jogadores diferentes não se bloqueiam.
func TestCarteirasDistintasEmParaleloEntreInstancias(t *testing.T) {
	c := novoCluster(t)

	const carteiras = 9
	ids := make([]string, carteiras)
	jogadores := make([]string, carteiras)
	for i := range ids {
		ids[i], jogadores[i] = c.abreCarteira(t, "100.00")
	}

	var wg sync.WaitGroup
	largada := make(chan struct{})
	status := make([]int, carteiras)
	for i := 0; i < carteiras; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chave, corpo := c.aposta(ids[i], jogadores[i], fmt.Sprintf("multi-par-%d", i), "30.00")
			<-largada
			status[i], _ = c.do(t, i, "POST", "/wagering/transactions", c.provedor,
				corpo, map[string]string{"Idempotency-Key": chave})
		}(i)
	}
	close(largada)
	wg.Wait()

	for i := range ids {
		if status[i] != http.StatusOK {
			t.Errorf("carteira %d devolveu %d", i, status[i])
			continue
		}
		if got := c.saldo(t, ids[i]); got != "70.00" {
			t.Errorf("carteira %d ficou com %q, esperado \"70.00\"", i, got)
		}
		if consistente, _ := c.reconcilia(t, ids[i]); !consistente {
			t.Errorf("carteira %d divergiu do ledger", i)
		}
	}
}

// TestSequenciaDistribuidaFechaNoLedger faz uma sequência de operações
// espalhadas pelas instâncias e confere, ao final, que o saldo armazenado é
// exatamente créditos menos débitos — a verificação que o desafio pede no fim.
func TestSequenciaDistribuidaFechaNoLedger(t *testing.T) {
	c := novoCluster(t)
	walletID, playerID := c.abreCarteira(t, "1000.00")

	operacoes := []struct {
		kind  string
		valor string
	}{
		{"BET", "100.00"}, {"WIN", "250.00"}, {"BET", "75.50"},
		{"LOSS", "0.00"}, {"BET", "24.50"}, {"WIN", "10.00"},
	}

	for i, op := range operacoes {
		chave, corpo := c.aposta(walletID, playerID, fmt.Sprintf("multi-seq-%d", i), op.valor)
		corpo["kind"] = op.kind

		status, raw := c.do(t, i, "POST", "/wagering/transactions", c.provedor,
			corpo, map[string]string{"Idempotency-Key": chave})
		if status != http.StatusOK {
			t.Fatalf("operação %d (%s %s) devolveu %d: %s", i, op.kind, op.valor, status, raw)
		}
	}

	// 1000 - 100 + 250 - 75.50 - 24.50 + 10
	if got := c.saldo(t, walletID); got != "1060.00" {
		t.Errorf("saldo = %q, esperado \"1060.00\"", got)
	}

	consistente, entradas := c.reconcilia(t, walletID)
	if !consistente {
		t.Error("o saldo armazenado não bate com créditos menos débitos do ledger")
	}
	// Abertura + 5 movimentações. O LOSS não produz lançamento.
	if entradas != 6 {
		t.Errorf("lançamentos = %d, esperado 6: LOSS não deveria criar lançamento", entradas)
	}
}
