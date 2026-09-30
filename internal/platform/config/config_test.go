package config_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonatancruz/jungle/internal/platform/config"
)

// chaves é o conjunto que Load consulta. O teste limpa todas antes de cada
// caso para não depender do ambiente de quem executa a suíte.
var chaves = []string{
	"APP_ENV", "APP_INSTANCE_ID", "LOG_LEVEL",
	"DATABASE_URL", "DATABASE_MAX_CONNS", "DATABASE_MIN_CONNS",
	"DATABASE_MAX_CONN_LIFETIME", "DATABASE_CONNECT_TIMEOUT", "DATABASE_STATEMENT_TIMEOUT",
	"HTTP_PORT", "HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT",
	"HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_SHUTDOWN_TIMEOUT",
}

func ambiente(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, k := range chaves {
		if valor, ok := os.LookupEnv(k); ok {
			t.Setenv(k, valor) // registra a restauração
			os.Unsetenv(k)
		}
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func TestCarregaComPadroes(t *testing.T) {
	ambiente(t, map[string]string{
		"DATABASE_URL": "postgres://user:pass@localhost:5432/jungle?sslmode=disable",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load devolveu erro: %v", err)
	}

	if cfg.Database.URL == "" {
		t.Error("DATABASE_URL não foi carregada")
	}
	if cfg.Database.MaxConns != 10 || cfg.Database.MinConns != 2 {
		t.Errorf("pool = %d/%d, esperado 10/2", cfg.Database.MaxConns, cfg.Database.MinConns)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("porta = %d, esperado 8080", cfg.HTTP.Port)
	}
	if cfg.App.Environment != "development" {
		t.Errorf("ambiente = %q, esperado \"development\"", cfg.App.Environment)
	}
	// O identificador de instância precisa existir mesmo sem variável: é o que
	// aparece no lease da outbox.
	if cfg.App.InstanceID == "" {
		t.Error("InstanceID ficou vazio sem APP_INSTANCE_ID")
	}
	if cfg.Database.StatementTimeout != 10*time.Second {
		t.Errorf("statement timeout = %v, esperado 10s", cfg.Database.StatementTimeout)
	}
}

func TestSobrescreveComAmbiente(t *testing.T) {
	ambiente(t, map[string]string{
		"DATABASE_URL":               "postgres://localhost/jungle",
		"DATABASE_MAX_CONNS":         "25",
		"DATABASE_MIN_CONNS":         "5",
		"DATABASE_STATEMENT_TIMEOUT": "3s",
		"HTTP_PORT":                  "9090",
		"HTTP_SHUTDOWN_TIMEOUT":      "45s",
		"APP_ENV":                    "production",
		"APP_INSTANCE_ID":            "instance-b",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load devolveu erro: %v", err)
	}

	if cfg.Database.MaxConns != 25 || cfg.Database.MinConns != 5 {
		t.Errorf("pool = %d/%d, esperado 25/5", cfg.Database.MaxConns, cfg.Database.MinConns)
	}
	if cfg.Database.StatementTimeout != 3*time.Second {
		t.Errorf("statement timeout = %v, esperado 3s", cfg.Database.StatementTimeout)
	}
	if cfg.HTTP.Port != 9090 || cfg.HTTP.ShutdownTimeout != 45*time.Second {
		t.Errorf("http = %d/%v, esperado 9090/45s", cfg.HTTP.Port, cfg.HTTP.ShutdownTimeout)
	}
	if cfg.App.Environment != "production" || cfg.App.InstanceID != "instance-b" {
		t.Errorf("app = %q/%q", cfg.App.Environment, cfg.App.InstanceID)
	}
}

func TestRecusaConfiguracaoInvalida(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		trec string
	}{
		{
			"sem DATABASE_URL",
			map[string]string{},
			"DATABASE_URL é obrigatória",
		},
		{
			"DATABASE_URL só com espaços",
			map[string]string{"DATABASE_URL": "   "},
			"DATABASE_URL é obrigatória",
		},
		{
			"pool mínimo maior que o máximo",
			map[string]string{
				"DATABASE_URL":       "postgres://localhost/jungle",
				"DATABASE_MIN_CONNS": "50",
				"DATABASE_MAX_CONNS": "5",
			},
			"não pode exceder",
		},
		{
			"pool máximo zerado",
			map[string]string{
				"DATABASE_URL":       "postgres://localhost/jungle",
				"DATABASE_MAX_CONNS": "0",
			},
			"ao menos 1",
		},
		{
			"porta fora do intervalo",
			map[string]string{
				"DATABASE_URL": "postgres://localhost/jungle",
				"HTTP_PORT":    "99999",
			},
			"HTTP_PORT fora do intervalo",
		},
		{
			"duração malformada",
			map[string]string{
				"DATABASE_URL":             "postgres://localhost/jungle",
				"DATABASE_CONNECT_TIMEOUT": "cinco segundos",
			},
			"não é duração válida",
		},
		{
			"duração não positiva",
			map[string]string{
				"DATABASE_URL":          "postgres://localhost/jungle",
				"HTTP_SHUTDOWN_TIMEOUT": "0s",
			},
			"precisa ser positiva",
		},
		{
			"inteiro malformado",
			map[string]string{
				"DATABASE_URL": "postgres://localhost/jungle",
				"HTTP_PORT":    "oitenta",
			},
			"não é inteiro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ambiente(t, tt.vars)

			cfg, err := config.Load()
			if !errors.Is(err, config.ErrInvalidConfig) {
				t.Fatalf("Load devolveu %v, esperado ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.trec) {
				t.Errorf("mensagem não menciona %q:\n%v", tt.trec, err)
			}
			if cfg != (config.Config{}) {
				t.Error("Load devolveu configuração utilizável junto com o erro")
			}
		})
	}
}

// TestReportaTodosOsProblemasDeUmaVez importa para quem está subindo o
// ambiente: descobrir um erro por execução é um ciclo desnecessariamente lento.
func TestReportaTodosOsProblemasDeUmaVez(t *testing.T) {
	ambiente(t, map[string]string{
		"DATABASE_URL":       "postgres://localhost/jungle",
		"DATABASE_MIN_CONNS": "50",
		"DATABASE_MAX_CONNS": "5",
		"HTTP_PORT":          "99999",
	})

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load aceitou configuração inválida")
	}
	msg := err.Error()
	for _, esperado := range []string{"DATABASE_MIN_CONNS", "HTTP_PORT"} {
		if !strings.Contains(msg, esperado) {
			t.Errorf("mensagem não menciona %s:\n%v", esperado, msg)
		}
	}
}
