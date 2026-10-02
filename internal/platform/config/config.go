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
	App       App
	Database  Database
	HTTP      HTTP
	Reference Reference
	SQS       SQS
	Outbox    Outbox
	Auth      Auth
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

// Auth traz a integração com o IdP externo.
type Auth struct {
	// IssuerURL é o emissor OIDC. A descoberta busca as chaves públicas a
	// partir dele.
	IssuerURL string

	// Audience é o identificador desta API no IdP. Tokens emitidos para outro
	// serviço do mesmo realm são recusados.
	Audience string

	// JWKSURL é onde buscar as chaves públicas, quando o endereço do emissor
	// não é alcançável pela aplicação.
	//
	// Em container isso é a regra, não a exceção. O Keycloak emite tokens com
	// iss igual à URL pela qual os clientes o alcançam — localhost:8081 — e o
	// documento de descoberta repete esse endereço no jwks_uri. Dentro da rede
	// do Compose, localhost:8081 é a própria aplicação, então seguir a
	// descoberta buscaria as chaves no lugar errado.
	//
	// Informar o JWKS explicitamente resolve sem adivinhação: a aplicação busca
	// onde consegue alcançar, e continua verificando o claim iss contra
	// IssuerURL. Vazio, a descoberta a partir do emissor é usada.
	JWKSURL string
}

// SQS traz o acesso à mensageria.
type SQS struct {
	// Endpoint aponta para o emulador local. Vazio usa o endpoint real da AWS,
	// resolvido pela região.
	Endpoint string
	Region   string

	// InboundQueueURL é a fila FIFO de operações recebidas de provedores.
	InboundQueueURL string

	// OutboundQueueURL é o destino dos eventos de integração publicados.
	OutboundQueueURL string

	// MaxMessages é quantas mensagens cada recebimento busca.
	MaxMessages int

	// WaitTime é o long polling. Ele troca varredura ocupada por espera no
	// servidor: sem isso o consumidor queimaria requisições contra fila vazia.
	WaitTime time.Duration

	// VisibilityTimeout é quanto tempo a mensagem fica invisível depois de
	// recebida. Precisa cobrir o processamento, ou a mensagem é reentregue
	// enquanto ainda está sendo tratada.
	VisibilityTimeout time.Duration
}

// Outbox traz a política de publicação dos eventos de integração.
type Outbox struct {
	// BatchSize limita quantos eventos uma varredura reivindica.
	BatchSize int

	// Lease é por quanto tempo um evento reivindicado fica reservado. Se a
	// instância morrer antes de publicar, ele volta à fila quando vencer.
	Lease time.Duration

	// PollInterval é o intervalo entre varreduras.
	PollInterval time.Duration

	// InitialBackoff e MaxBackoff governam a nova tentativa após falha de
	// publicação. Não há limite de tentativas: um evento financeiro que não
	// consegue ser publicado é problema operacional, e descartá-lo perderia
	// dado. O atraso da outbox é a métrica que denuncia a situação.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// Reference traz a política de espera por referências ainda indisponíveis.
//
// Uma reversão pode chegar antes da transação que ela desfaz. O serviço aceita
// a operação, registra a pendência e tenta de novo com recuo exponencial. O
// prazo total e o teto de tentativas existem para que uma referência que nunca
// chegue termine em recusa auditável, e não em pendência eterna.
type Reference struct {
	// TTL é o prazo total de espera. Esgotado, a operação é recusada com
	// código de referência não encontrada.
	TTL time.Duration

	// MaxAttempts limita as tentativas. Vale em conjunto com o TTL: o que
	// vencer primeiro encerra a espera.
	MaxAttempts int

	// InitialBackoff é o intervalo da primeira nova tentativa. Ele dobra a
	// cada falha até MaxBackoff.
	InitialBackoff time.Duration

	// MaxBackoff limita o crescimento do recuo, para que uma pendência longa
	// não fique com intervalos de horas.
	MaxBackoff time.Duration

	// PollInterval é o intervalo entre varreduras do worker.
	PollInterval time.Duration

	// BatchSize limita quantas pendências uma varredura reivindica.
	BatchSize int
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

	cfg.Database = loadDatabase(collect)

	authIssuer, err := required("AUTH_ISSUER_URL")
	collect(err)
	cfg.Auth.IssuerURL = authIssuer
	cfg.Auth.Audience = envOr("AUTH_AUDIENCE", "jungle-api")
	cfg.Auth.JWKSURL = envOr("AUTH_JWKS_URL", "")

	cfg.SQS.Endpoint = envOr("SQS_ENDPOINT", "")
	cfg.SQS.Region = envOr("AWS_REGION", "us-east-1")
	cfg.SQS.InboundQueueURL = envOr("SQS_INBOUND_QUEUE_URL", "")
	cfg.SQS.OutboundQueueURL = envOr("SQS_OUTBOUND_QUEUE_URL", "")
	cfg.SQS.MaxMessages, err = envInt("SQS_MAX_MESSAGES", 10)
	collect(err)
	cfg.SQS.WaitTime, err = envDuration("SQS_WAIT_TIME", 20*time.Second)
	collect(err)
	cfg.SQS.VisibilityTimeout, err = envDuration("SQS_VISIBILITY_TIMEOUT", 30*time.Second)
	collect(err)

	cfg.Outbox.BatchSize, err = envInt("OUTBOX_BATCH_SIZE", 50)
	collect(err)
	cfg.Outbox.Lease, err = envDuration("OUTBOX_LEASE", 30*time.Second)
	collect(err)
	cfg.Outbox.PollInterval, err = envDuration("OUTBOX_POLL_INTERVAL", time.Second)
	collect(err)
	cfg.Outbox.InitialBackoff, err = envDuration("OUTBOX_INITIAL_BACKOFF", time.Second)
	collect(err)
	cfg.Outbox.MaxBackoff, err = envDuration("OUTBOX_MAX_BACKOFF", time.Minute)
	collect(err)

	cfg.Reference.TTL, err = envDuration("REFERENCE_TTL", 15*time.Minute)
	collect(err)
	cfg.Reference.MaxAttempts, err = envInt("REFERENCE_MAX_ATTEMPTS", 10)
	collect(err)
	cfg.Reference.InitialBackoff, err = envDuration("REFERENCE_INITIAL_BACKOFF", 2*time.Second)
	collect(err)
	cfg.Reference.MaxBackoff, err = envDuration("REFERENCE_MAX_BACKOFF", 2*time.Minute)
	collect(err)
	cfg.Reference.PollInterval, err = envDuration("REFERENCE_POLL_INTERVAL", 2*time.Second)
	collect(err)
	cfg.Reference.BatchSize, err = envInt("REFERENCE_BATCH_SIZE", 50)
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

// LoadDatabase carrega e valida apenas a configuração de banco.
//
// Existe para o migrator, que só precisa do endereço do banco. Fazê-lo passar
// por Load o obrigaria a declarar emissor de token e filas — variáveis que ele
// não usa — e `go run ./cmd/migrate up` falharia por configuração irrelevante
// ao que o comando faz.
func LoadDatabase() (Database, error) {
	var problems []string
	collect := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}

	db := loadDatabase(collect)
	if len(problems) == 0 {
		problems = append(problems, db.validate()...)
	}
	if len(problems) > 0 {
		return Database{}, fmt.Errorf("%w:\n  - %s", ErrInvalidConfig, strings.Join(problems, "\n  - "))
	}
	return db, nil
}

// loadDatabase lê a seção de banco do ambiente.
func loadDatabase(collect func(error)) Database {
	var (
		db  Database
		err error
	)

	db.URL, err = required("DATABASE_URL")
	collect(err)
	db.MaxConns, err = envInt32("DATABASE_MAX_CONNS", 10)
	collect(err)
	db.MinConns, err = envInt32("DATABASE_MIN_CONNS", 2)
	collect(err)
	db.MaxConnLifetime, err = envDuration("DATABASE_MAX_CONN_LIFETIME", time.Hour)
	collect(err)
	db.ConnectTimeout, err = envDuration("DATABASE_CONNECT_TIMEOUT", 5*time.Second)
	collect(err)
	db.StatementTimeout, err = envDuration("DATABASE_STATEMENT_TIMEOUT", 10*time.Second)
	collect(err)

	return db
}

// validate confere as regras da seção de banco.
func (d Database) validate() []string {
	var problems []string

	if d.MaxConns < 1 {
		problems = append(problems, "DATABASE_MAX_CONNS precisa ser ao menos 1")
	}
	if d.MinConns < 0 {
		problems = append(problems, "DATABASE_MIN_CONNS não pode ser negativo")
	}
	if d.MinConns > d.MaxConns {
		problems = append(problems, fmt.Sprintf(
			"DATABASE_MIN_CONNS (%d) não pode exceder DATABASE_MAX_CONNS (%d)",
			d.MinConns, d.MaxConns))
	}
	return problems
}

// validate reúne as regras que dependem de mais de um campo. Devolve todos os
// problemas de uma vez: quem está subindo o ambiente prefere a lista inteira a
// descobrir um erro por execução.
func (c Config) validate() []string {
	problems := c.Database.validate()

	if c.Auth.Audience == "" {
		problems = append(problems, "AUTH_AUDIENCE não pode ser vazia")
	}
	// As duas filas são obrigatórias porque o consumidor e o publicador sobem
	// sempre, sem condicional. Vazias, o consumidor falharia a cada ciclo e o
	// publicador a cada evento, cada um gerando uma linha de erro por segundo
	// para sempre — um processo que parece de pé e não processa nada. Recusar
	// na partida é o comportamento que este pacote promete.
	if c.SQS.InboundQueueURL == "" {
		problems = append(problems, "SQS_INBOUND_QUEUE_URL é obrigatória")
	}
	if c.SQS.OutboundQueueURL == "" {
		problems = append(problems, "SQS_OUTBOUND_QUEUE_URL é obrigatória")
	}
	if c.SQS.MaxMessages < 1 || c.SQS.MaxMessages > 10 {
		problems = append(problems, fmt.Sprintf(
			"SQS_MAX_MESSAGES precisa estar entre 1 e 10, recebido %d", c.SQS.MaxMessages))
	}
	if c.SQS.WaitTime > 20*time.Second {
		problems = append(problems, fmt.Sprintf(
			"SQS_WAIT_TIME não pode exceder 20s, limite do long polling; recebido %v", c.SQS.WaitTime))
	}
	if c.Outbox.BatchSize < 1 {
		problems = append(problems, "OUTBOX_BATCH_SIZE precisa ser ao menos 1")
	}
	if c.Outbox.InitialBackoff > c.Outbox.MaxBackoff {
		problems = append(problems, fmt.Sprintf(
			"OUTBOX_INITIAL_BACKOFF (%v) não pode exceder OUTBOX_MAX_BACKOFF (%v)",
			c.Outbox.InitialBackoff, c.Outbox.MaxBackoff))
	}
	if c.Reference.MaxAttempts < 1 {
		problems = append(problems, "REFERENCE_MAX_ATTEMPTS precisa ser ao menos 1")
	}
	if c.Reference.BatchSize < 1 {
		problems = append(problems, "REFERENCE_BATCH_SIZE precisa ser ao menos 1")
	}
	if c.Reference.InitialBackoff > c.Reference.MaxBackoff {
		problems = append(problems, fmt.Sprintf(
			"REFERENCE_INITIAL_BACKOFF (%v) não pode exceder REFERENCE_MAX_BACKOFF (%v)",
			c.Reference.InitialBackoff, c.Reference.MaxBackoff))
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
