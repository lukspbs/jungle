package app_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/domain/money"
)

// relogioFixo devolve sempre o mesmo instante, para que os testes possam
// afirmar exatamente o que foi gravado em eventos e lançamentos.
type relogioFixo struct{ instante time.Time }

func (r relogioFixo) Now() time.Time { return r.instante }

// idsSequenciais gera identificadores determinísticos dentro de um teste.
//
// O prefixo é aleatório por instância para que testes distintos ocupem espaços
// de identificador distintos: a suíte compartilha um banco e nunca limpa
// tabelas, porque o ledger recusa DELETE e TRUNCATE.
type idsSequenciais struct {
	prefixo [8]byte
	n       atomic.Uint64
}

// novosIDs cria um gerador com espaço próprio.
func novosIDs(t *testing.T) *idsSequenciais {
	t.Helper()
	g := &idsSequenciais{}
	semente := uuid.New()
	copy(g.prefixo[:], semente[:8])
	return g
}

func (g *idsSequenciais) New() (uuid.UUID, error) {
	v := g.n.Add(1)
	var b [16]byte
	copy(b[:8], g.prefixo[:])
	for i := 0; i < 8; i++ {
		b[15-i] = byte(v >> (8 * i))
	}
	// Marca versão 7 e variante RFC 4122, para que o valor seja um UUID válido.
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return uuid.UUID(b), nil
}

func brlOf(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}

// jsonDecode é o atalho de desserialização usado pelas asserções sobre
// payloads gravados na outbox.
func jsonDecode(raw []byte, destino any) error {
	return json.Unmarshal(raw, destino)
}

// relogioAjustavel permite avançar o tempo nos testes que dependem de prazos.
//
// O worker de referências só reivindica o que já venceu, e um relógio fixo
// nunca faz um backoff vencer. Avançar explicitamente mantém o teste
// determinístico — nada de dormir esperando o relógio real.
type relogioAjustavel struct {
	mu sync.Mutex
	t  time.Time
}

func novoRelogio(inicio time.Time) *relogioAjustavel {
	return &relogioAjustavel{t: inicio}
}

func (r *relogioAjustavel) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *relogioAjustavel) Avanca(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = r.t.Add(d)
}

// loggerDeTeste descarta a saída: a suíte afirma sobre estado persistido, não
// sobre linhas de log.
func loggerDeTeste() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
