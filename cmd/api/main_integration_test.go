package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

// portaLivre reserva uma porta efêmera para que execuções paralelas da suíte
// não disputem a porta padrão.
func portaLivre(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func ambienteValido(t *testing.T) int {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL não definida: teste de composição pulado")
	}

	porta := portaLivre(t)
	t.Setenv("DATABASE_URL", url)
	t.Setenv("HTTP_PORT", fmt.Sprint(porta))
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("REFERENCE_POLL_INTERVAL", "50ms")
	return porta
}

// TestGrafoDeDependenciasEhValido confere a composição sem construir nada.
//
// É a checagem mais barata: ela pega dependência faltando, ciclo e construtor
// com assinatura incompatível antes de qualquer conexão ser aberta.
func TestGrafoDeDependenciasEhValido(t *testing.T) {
	ambienteValido(t)

	if err := fx.ValidateApp(Modules(), fx.WithLogger(func() fxevent.Logger {
		return fxevent.NopLogger
	})); err != nil {
		t.Fatalf("grafo inválido: %v", err)
	}
}

// TestAplicacaoSobeEDesce exercita o ciclo de vida completo: start, requisição
// real, e encerramento ordenado.
func TestAplicacaoSobeEDesce(t *testing.T) {
	porta := ambienteValido(t)

	aplicacao := fx.New(Modules(), fx.WithLogger(func() fxevent.Logger {
		return fxevent.NopLogger
	}))

	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStart()
	if err := aplicacao.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", porta)
	cliente := &http.Client{Timeout: 5 * time.Second}

	t.Run("o servidor atende", func(t *testing.T) {
		res, err := cliente.Get(base + "/health/live")
		if err != nil {
			t.Fatalf("GET /health/live: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("status = %d", res.StatusCode)
		}
	})

	t.Run("as dependências respondem", func(t *testing.T) {
		res, err := cliente.Get(base + "/health/ready")
		if err != nil {
			t.Fatalf("GET /health/ready: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("status = %d, esperado 200: o banco deveria estar acessível", res.StatusCode)
		}
	})

	// O worker de referências está rodando: dar tempo de ao menos uma varredura
	// acontecer garante que o encerramento pegue uma goroutine viva, que é o
	// caso que interessa testar.
	time.Sleep(150 * time.Millisecond)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelStop()
	if err := aplicacao.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	t.Run("o servidor parou de atender", func(t *testing.T) {
		_, err := cliente.Get(base + "/health/live")
		if err == nil {
			t.Error("o servidor continuou atendendo depois do encerramento")
		}
	})

	t.Run("a porta foi liberada", func(t *testing.T) {
		// Conseguir escutar na mesma porta prova que o listener foi fechado —
		// recurso liberado, não apenas abandonado.
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", porta))
		if err != nil {
			t.Errorf("a porta %d continua ocupada: %v", porta, err)
			return
		}
		l.Close()
	})
}

// TestConfiguracaoInvalidaImpedeASubida confere que a validação acontece na
// inicialização, e não na primeira operação financeira.
func TestConfiguracaoInvalidaImpedeASubida(t *testing.T) {
	ambienteValido(t)
	t.Setenv("DATABASE_MIN_CONNS", "99")
	t.Setenv("DATABASE_MAX_CONNS", "2")

	aplicacao := fx.New(Modules(), fx.WithLogger(func() fxevent.Logger {
		return fxevent.NopLogger
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := aplicacao.Start(ctx)
	if err == nil {
		_ = aplicacao.Stop(ctx)
		t.Fatal("a aplicação subiu com configuração inválida")
	}
}

// TestBancoIndisponivelImpedeASubida confere que uma dependência fora do ar
// impede a aplicação de subir, em vez de deixá-la subir para falhar na primeira
// aposta.
func TestBancoIndisponivelImpedeASubida(t *testing.T) {
	ambienteValido(t)
	t.Setenv("DATABASE_URL", "postgres://ninguem:nada@127.0.0.1:1/inexistente?sslmode=disable")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "1s")

	aplicacao := fx.New(Modules(), fx.WithLogger(func() fxevent.Logger {
		return fxevent.NopLogger
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := aplicacao.Start(ctx); err == nil {
		_ = aplicacao.Stop(ctx)
		t.Fatal("a aplicação subiu com o banco indisponível")
	} else if !errors.Is(err, context.DeadlineExceeded) && err.Error() == "" {
		t.Fatalf("erro inesperado: %v", err)
	}
}
