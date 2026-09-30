// Package config carrega a configuração a partir do ambiente.
//
// A validação acontece no carregamento e não no uso: o processo recusa subir
// com configuração incompleta, em vez de falhar no meio de uma operação
// financeira. É essa checagem que o hook de inicialização do Fx executa.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidConfig indica configuração ausente ou malformada.
var ErrInvalidConfig = errors.New("config: configuração inválida")

// Config reúne a configuração do processo.
type Config struct {
	App      App
	Database Database
	HTTP     HTTP
}

// App traz os metadados do processo.
type App struct {
	// Environment identifica o ambiente para logs e métricas.
	Environment string

	// InstanceID distingue instâncias no mesmo cluster. É o que aparece no
	// lease da outbox, permitindo ver qual processo assumiu qual evento.
	InstanceID string

	// LogLevel controla o nível mínimo registrado.
	LogLevel string
}

// Database traz a conexão com o PostgreSQL.
type Database struct {
	// URL é a connection string no formato aceito pelo pgx.
	URL string

	// MaxConns limita o pool. Cada instância precisa do seu próprio pool: o
	// desafio exige que as garantias valham entre processos independentes.
	MaxConns int32

	// MinConns mantém conexões quentes para não pagar handshake no caminho
	// quente de uma aposta.
	MinConns int32

	// MaxConnLifetime recicla conexões para evitar acúmulo de estado no
	// servidor.
	MaxConnLifetime time.Duration

	// ConnectTimeout limita a espera por uma conexão nova.
	ConnectTimeout time.Duration

	// StatementTimeout limita a duração de um comando no servidor. Protege
	// contra um lock de carteira segurado indefinidamente por um cliente
	// travado.
	StatementTimeout time.Duration
}

// HTTP traz a configuração do servidor.
type HTTP struct {
	Port              int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// ShutdownTimeout é o prazo para concluir requisições em andamento após
	// SIGTERM antes de encerrar à força.
	ShutdownTimeout time.Duration
}

// Load lê a configuração do ambiente e a valida.
func Load() (Config, error) {
	var problems []string
	collect := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}

	cfg := Config{
		App: App{
			Environment: envOr("APP_ENV", "development"),
			InstanceID:  envOr("APP_INSTANCE_ID", defaultInstanceID()),
			LogLevel:    envOr("LOG_LEVEL", "info"),
		},
	}

	dbURL, err := required("DATABASE_URL")
	collect(err)
	cfg.Database.URL = dbURL

	cfg.Database.MaxConns, err = envInt32("DATABASE_MAX_CONNS", 10)
	collect(err)
	cfg.Database.MinConns, err = envInt32("DATABASE_MIN_CONNS", 2)
	collect(err)
	cfg.Database.MaxConnLifetime, err = envDuration("DATABASE_MAX_CONN_LIFETIME", time.Hour)
	collect(err)
	cfg.Database.ConnectTimeout, err = envDuration("DATABASE_CONNECT_TIMEOUT", 5*time.Second)
	collect(err)
	cfg.Database.StatementTimeout, err = envDuration("DATABASE_STATEMENT_TIMEOUT", 10*time.Second)
	collect(err)

	cfg.HTTP.Port, err = envInt("HTTP_PORT", 8080)
	collect(err)
	cfg.HTTP.ReadHeaderTimeout, err = envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second)
	collect(err)
	cfg.HTTP.ReadTimeout, err = envDuration("HTTP_READ_TIMEOUT", 15*time.Second)
	collect(err)
	cfg.HTTP.WriteTimeout, err = envDuration("HTTP_WRITE_TIMEOUT", 15*time.Second)
	collect(err)
	cfg.HTTP.IdleTimeout, err = envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second)
	collect(err)
	cfg.HTTP.ShutdownTimeout, err = envDuration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second)
	collect(err)

	if len(problems) == 0 {
		problems = append(problems, cfg.validate()...)
	}
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("%w:\n  - %s", ErrInvalidConfig, strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

// validate reúne as regras que dependem de mais de um campo. Devolve todos os
// problemas de uma vez: quem está subindo o ambiente prefere a lista inteira a
// descobrir um erro por execução.
func (c Config) validate() []string {
	var problems []string

	if c.Database.MaxConns < 1 {
		problems = append(problems, "DATABASE_MAX_CONNS precisa ser ao menos 1")
	}
	if c.Database.MinConns < 0 {
		problems = append(problems, "DATABASE_MIN_CONNS não pode ser negativo")
	}
	if c.Database.MinConns > c.Database.MaxConns {
		problems = append(problems, fmt.Sprintf(
			"DATABASE_MIN_CONNS (%d) não pode exceder DATABASE_MAX_CONNS (%d)",
			c.Database.MinConns, c.Database.MaxConns))
	}
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		problems = append(problems, fmt.Sprintf("HTTP_PORT fora do intervalo válido: %d", c.HTTP.Port))
	}
	if c.App.InstanceID == "" {
		problems = append(problems, "APP_INSTANCE_ID não pode ser vazio")
	}
	return problems
}

func required(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s é obrigatória", key)
	}
	return v, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q não é inteiro", key, raw)
	}
	return v, nil
}

func envInt32(key string, fallback int32) (int32, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %q não é inteiro de 32 bits", key, raw)
	}
	return int32(v), nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q não é duração válida (use 5s, 100ms, 1h)", key, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s: duração precisa ser positiva, recebido %q", key, raw)
	}
	return v, nil
}

// defaultInstanceID deriva um identificador do hostname, que no Docker Compose
// e no Kubernetes já é único por instância.
func defaultInstanceID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "unknown"
}
