// Package app orquestra os casos de uso.
//
// A camada conhece o domínio e a persistência, mas não conhece HTTP, SQS nem
// Fx: ela recebe comandos e devolve resultados. É isso que permite que a mesma
// operação chegue por HTTP e por SQS compartilhando o caso de uso e, com ele,
// as garantias de idempotência.
//
// A dependência direta do pacote postgres é deliberada. O desafio exige que
// transações, locks e constraints permaneçam explícitos e verificáveis; uma
// camada de interfaces sobre os repositórios esconderia justamente o que
// precisa estar visível — onde está o FOR UPDATE e o que está dentro de qual
// transação. O que precisa ficar independente de persistência é o domínio, e
// ele está.
package app

import (
	"time"

	"github.com/google/uuid"
)

// Clock fornece o instante atual.
//
// O relógio é injetado, e não lido de time.Now espalhado pelo código, porque
// os instantes entram em eventos, lançamentos e prazos de retentativa: um teste
// precisa poder fixá-los para afirmar o que foi gravado.
type Clock interface {
	Now() time.Time
}

// SystemClock é o relógio de produção. Devolve sempre em UTC, de modo que o
// fuso da instância nunca chega ao banco nem ao payload de um evento.
type SystemClock struct{}

// Now devolve o instante atual em UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// IDs gera identificadores de agregados, lançamentos e eventos.
type IDs interface {
	New() (uuid.UUID, error)
}

// UUIDv7 gera identificadores ordenáveis pelo tempo.
//
// A versão 7 embute o instante de criação no prefixo, então os identificadores
// crescem monotonicamente. Isso mantém as inserções no fim do índice em vez de
// espalhadas por ele, o que importa nas tabelas que mais crescem — transações,
// lançamentos e outbox.
type UUIDv7 struct{}

// New devolve um UUID versão 7.
func (UUIDv7) New() (uuid.UUID, error) { return uuid.NewV7() }
