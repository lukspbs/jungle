// Package worker governa o ciclo de vida das rotinas de fundo.
package worker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Loop é uma rotina que roda até o contexto ser cancelado.
type Loop interface {
	Run(ctx context.Context) error
}

// Runner liga um Loop ao ciclo de vida da aplicação.
//
// O encerramento é observável: Stop cancela o contexto e espera a rotina
// confirmar que terminou, dentro de um prazo. Sem essa espera, o processo
// morreria com trabalho pela metade e o operador não teria como saber se o
// shutdown foi limpo.
type Runner struct {
	name    string
	loop    Loop
	timeout time.Duration

	cancel context.CancelFunc
	done   chan error
}

// New monta o runner.
func New(name string, loop Loop, shutdownTimeout time.Duration) *Runner {
	return &Runner{name: name, loop: loop, timeout: shutdownTimeout}
}

// Name identifica o worker em logs e erros.
func (r *Runner) Name() string { return r.name }

// Start dispara a rotina.
//
// O contexto recebido é o da inicialização e tem prazo próprio; a rotina não
// pode herdá-lo, ou morreria assim que o start terminasse. Por isso o contexto
// de execução nasce aqui, desvinculado, e só é cancelado por Stop.
func (r *Runner) Start(context.Context) error {
	if r.done != nil {
		return fmt.Errorf("worker %s: já iniciado", r.name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan error, 1)

	go func() {
		defer close(r.done)
		if err := r.loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			r.done <- err
		}
	}()
	return nil
}

// Stop interrompe a rotina e espera a confirmação.
//
// Esgotado o prazo, devolve erro em vez de bloquear para sempre: o operador
// precisa saber que o worker não encerrou limpo, e o processo precisa
// conseguir morrer.
func (r *Runner) Stop(ctx context.Context) error {
	if r.done == nil {
		return nil
	}
	r.cancel()

	prazo, cancelar := context.WithTimeout(ctx, r.timeout)
	defer cancelar()

	select {
	case err := <-r.done:
		r.done = nil
		if err != nil {
			return fmt.Errorf("worker %s terminou com erro: %w", r.name, err)
		}
		return nil
	case <-prazo.Done():
		return fmt.Errorf("worker %s não confirmou encerramento em %v", r.name, r.timeout)
	}
}
