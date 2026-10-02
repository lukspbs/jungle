package scenarios

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// EnvContainerControl autoriza o teste a parar e religar containers.
//
// Fica atrás de uma variável porque mexer no ambiente é efeito colateral forte
// para uma suíte: quem roda `go test ./...` numa máquina de desenvolvimento não
// espera que uma instância seja derrubada no meio.
const EnvContainerControl = "TEST_ALLOW_CONTAINER_CONTROL"

func exigeControleDeContainer(t *testing.T) {
	t.Helper()
	if os.Getenv(EnvContainerControl) != "1" {
		t.Skipf("%s não é 1: cenário de recuperação pulado", EnvContainerControl)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker não encontrado")
	}
}

func compose(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "--profile", "multi"}, args...)...)
	cmd.Dir = raizDoProjeto(t)
	if saida, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, saida)
	}
}

// iniciadoEm devolve o instante em que o container subiu.
//
// Serve para o teste provar a própria premissa: um reinício que não aconteceu
// deixaria o cenário passar por engano, afirmando resiliência que não foi
// exercitada.
func iniciadoEm(t *testing.T, container string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	saida, err := exec.CommandContext(ctx, "docker", "inspect", container,
		"--format", "{{.State.StartedAt}}").Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", container, err)
	}
	return strings.TrimSpace(string(saida))
}

// raizDoProjeto sobe até encontrar o docker-compose.yml.
func raizDoProjeto(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(dir + "/docker-compose.yml"); err == nil {
			return dir
		}
		pai := dir[:strings.LastIndex(dir, "/")]
		if pai == "" || pai == dir {
			t.Fatal("docker-compose.yml não encontrado")
		}
		dir = pai
	}
}

// esperaAte repete a verificação até ela passar ou o prazo vencer.
func esperaAte(t *testing.T, prazo time.Duration, descricao string, condicao func() bool) {
	t.Helper()
	limite := time.Now().Add(prazo)
	for time.Now().Before(limite) {
		if condicao() {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("prazo de %v esgotado esperando: %s", prazo, descricao)
}

func (c cluster) transacao(t *testing.T, id string) (estado, failureCode string) {
	t.Helper()
	status, corpo := c.do(t, 0, "GET", "/wagering/transactions/"+id, c.provedor, nil, nil)
	if status != http.StatusOK {
		return "", ""
	}
	var res struct {
		Status      string `json:"status"`
		FailureCode string `json:"failureCode"`
	}
	if err := json.Unmarshal(corpo, &res); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	return res.Status, res.FailureCode
}

// TestPendenciaResolvidaPorOutraInstancia é o cenário 8 do desafio na sua forma
// mais forte: a instância que registrou a pendência é derrubada, e outra assume.
//
// É isto que prova que o serviço não depende de uma instância específica: o
// estado vive no banco, a reivindicação é por SKIP LOCKED, e qualquer processo
// pode continuar o trabalho de qualquer outro.
func TestPendenciaResolvidaPorOutraInstancia(t *testing.T) {
	exigeControleDeContainer(t)
	c := novoCluster(t)

	walletID, playerID := c.abreCarteira(t, "1000.00")

	// A reversão chega antes da aposta, pela primeira instância.
	chaveEstorno, corpoEstorno := c.aposta(walletID, playerID, "rec-estorno", "60.00")
	corpoEstorno["kind"] = "REFUND"
	corpoEstorno["referenceExternalTransactionId"] = c.prefixo + "-rec-aposta"

	status, raw := c.do(t, 0, "POST", "/wagering/transactions", c.provedor,
		corpoEstorno, map[string]string{"Idempotency-Key": chaveEstorno})
	if status != http.StatusAccepted {
		t.Fatalf("estorno devolveu %d, esperado 202: %s", status, raw)
	}
	var pendente struct {
		TransactionID string `json:"transactionId"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(raw, &pendente); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if pendente.Status != "PENDING_REFERENCE" {
		t.Fatalf("estado = %q, esperado PENDING_REFERENCE", pendente.Status)
	}

	// A instância que registrou a pendência sai do ar.
	compose(t, "stop", "api")
	t.Cleanup(func() { compose(t, "start", "api") })

	// A aposta chega por outra instância.
	chaveAposta, corpoAposta := c.aposta(walletID, playerID, "rec-aposta", "60.00")
	status, raw = c.do(t, 1, "POST", "/wagering/transactions", c.provedor,
		corpoAposta, map[string]string{"Idempotency-Key": chaveAposta})
	if status != http.StatusOK {
		t.Fatalf("aposta devolveu %d: %s", status, raw)
	}
	if got := c.saldoVia(t, 1, walletID); got != "940.00" {
		t.Fatalf("saldo após a aposta = %q", got)
	}

	// Alguma das instâncias restantes precisa concluir a pendência.
	esperaAte(t, 30*time.Second, "a pendência ser resolvida por outra instância", func() bool {
		estado, _ := c.transacaoVia(t, 1, pendente.TransactionID)
		return estado == "PROCESSED"
	})

	if got := c.saldoVia(t, 1, walletID); got != "1000.00" {
		t.Errorf("saldo = %q, esperado \"1000.00\": o estorno não foi aplicado", got)
	}

	consistente, _ := c.reconciliaVia(t, 1, walletID)
	if !consistente {
		t.Error("divergência entre o saldo e o ledger depois da recuperação")
	}

	// A instância derrubada volta e a idempotência continua valendo: um reenvio
	// por ela devolve o resultado original, sem reaplicar nada.
	compose(t, "start", "api")
	esperaAte(t, 60*time.Second, "a instância voltar a responder", func() bool {
		res, err := c.cliente.Get(c.instancias[0] + "/health/ready")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == http.StatusOK
	})

	status, raw = c.do(t, 0, "POST", "/wagering/transactions", c.provedor,
		corpoAposta, map[string]string{"Idempotency-Key": chaveAposta})
	if status != http.StatusOK {
		t.Fatalf("reenvio após o religamento devolveu %d: %s", status, raw)
	}
	var replay struct {
		IdempotentReplay bool `json:"idempotentReplay"`
		Balance          struct {
			Amount string `json:"amount"`
		} `json:"balance"`
	}
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if !replay.IdempotentReplay {
		t.Error("o reenvio após o religamento não foi reconhecido como repetição")
	}
	// O saldo devolvido é o do processamento original, não o atual.
	if replay.Balance.Amount != "940.00" {
		t.Errorf("replay devolveu %q, esperado o saldo original \"940.00\"", replay.Balance.Amount)
	}
	if got := c.saldo(t, walletID); got != "1000.00" {
		t.Errorf("saldo = %q: o reenvio movimentou a carteira", got)
	}
}

// TestIdempotenciaSobreviveAoReinicio confere que o estado de idempotência é do
// banco e não da memória de um processo.
func TestIdempotenciaSobreviveAoReinicio(t *testing.T) {
	exigeControleDeContainer(t)
	c := novoCluster(t)

	walletID, playerID := c.abreCarteira(t, "500.00")
	chave, corpo := c.aposta(walletID, playerID, "rec-idem", "120.00")

	status, raw := c.do(t, 0, "POST", "/wagering/transactions", c.provedor,
		corpo, map[string]string{"Idempotency-Key": chave})
	if status != http.StatusOK {
		t.Fatalf("aposta devolveu %d: %s", status, raw)
	}
	var original struct {
		TransactionID string `json:"transactionId"`
	}
	_ = json.Unmarshal(raw, &original)

	// Todas as instâncias são reiniciadas: nenhuma memória sobrevive.
	containers := []string{"jungle-api-1", "jungle-api-2-1", "jungle-api-3-1"}
	antes := make([]string, len(containers))
	for i, c := range containers {
		antes[i] = iniciadoEm(t, c)
	}

	compose(t, "restart", "api", "api-2", "api-3")

	// Sem esta conferência o teste passaria mesmo que o reinício tivesse
	// falhado em silêncio, afirmando uma resiliência que não foi exercitada.
	for i, c := range containers {
		if depois := iniciadoEm(t, c); depois == antes[i] {
			t.Fatalf("%s não reiniciou: continua iniciado em %s", c, depois)
		}
	}
	esperaAte(t, 90*time.Second, "as instâncias voltarem", func() bool {
		for _, url := range c.instancias {
			res, err := c.cliente.Get(url + "/health/ready")
			if err != nil {
				return false
			}
			ok := res.StatusCode == http.StatusOK
			res.Body.Close()
			if !ok {
				return false
			}
		}
		return true
	})

	// O reenvio, agora por outra instância, continua sendo replay.
	status, raw = c.do(t, 2, "POST", "/wagering/transactions", c.provedor,
		corpo, map[string]string{"Idempotency-Key": chave})
	if status != http.StatusOK {
		t.Fatalf("reenvio devolveu %d: %s", status, raw)
	}
	var depois struct {
		TransactionID    string `json:"transactionId"`
		IdempotentReplay bool   `json:"idempotentReplay"`
	}
	if err := json.Unmarshal(raw, &depois); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if !depois.IdempotentReplay {
		t.Error("a idempotência não sobreviveu ao reinício: o reenvio foi tratado como nova operação")
	}
	if depois.TransactionID != original.TransactionID {
		t.Errorf("o reenvio criou outra transação: %s != %s", depois.TransactionID, original.TransactionID)
	}
	if got := c.saldo(t, walletID); got != "380.00" {
		t.Errorf("saldo = %q, esperado \"380.00\": houve movimentação duplicada", got)
	}

	consistente, _ := c.reconcilia(t, walletID)
	if !consistente {
		t.Error("divergência entre o saldo e o ledger depois do reinício")
	}
}

// As variantes "Via" permitem consultar por uma instância específica, já que
// durante o cenário a primeira pode estar fora do ar.

func (c cluster) saldoVia(t *testing.T, instancia int, walletID string) string {
	t.Helper()
	status, corpo := c.do(t, instancia, "GET", "/wallets/"+walletID, c.interno, nil, nil)
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

func (c cluster) transacaoVia(t *testing.T, instancia int, id string) (estado, failureCode string) {
	t.Helper()
	status, corpo := c.do(t, instancia, "GET", "/wagering/transactions/"+id, c.provedor, nil, nil)
	if status != http.StatusOK {
		return "", ""
	}
	var res struct {
		Status      string `json:"status"`
		FailureCode string `json:"failureCode"`
	}
	if err := json.Unmarshal(corpo, &res); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	return res.Status, res.FailureCode
}

func (c cluster) reconciliaVia(t *testing.T, instancia int, walletID string) (bool, int) {
	t.Helper()
	status, corpo := c.do(t, instancia, "POST", "/wallets/"+walletID+"/reconciliation", c.interno, nil, nil)
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
