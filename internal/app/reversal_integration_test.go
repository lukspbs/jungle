package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/wagering"
)

// reverte monta o comando de reversão apontando para uma operação anterior.
func (c cenario) reverte(kind wagering.Kind, valor, externalID, referencia string) app.ProcessWagerCommand {
	cmd := c.comando(kind, valor, externalID)
	cmd.ReferenceExternalTransactionID = c.prefixo + "-" + referencia
	return cmd
}

func TestEstornoDevolveAApostaIntegralmente(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}
	if got := c.saldo(t); got.String() != "975.00" {
		t.Fatalf("saldo após aposta = %q", got)
	}

	res, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "aposta"))
	if err != nil {
		t.Fatalf("estorno: %v", err)
	}
	if res.Status != wagering.Processed {
		t.Fatalf("estado = %v/%v", res.Status, res.FailureCode)
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo após estorno = %q, esperado \"1000.00\"", got)
	}
}

func TestRollbackDesfazCadaTipoNoSentidoOposto(t *testing.T) {
	t.Run("de uma aposta credita", func(t *testing.T) {
		c := novoCenario(t, "1000.00")
		ctx := context.Background()
		if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "40.00", "aposta")); err != nil {
			t.Fatalf("aposta: %v", err)
		}
		res, err := c.processar.Execute(ctx, c.reverte(wagering.Rollback, "40.00", "rb", "aposta"))
		if err != nil || res.Status != wagering.Processed {
			t.Fatalf("rollback: %v / %v", err, res.Status)
		}
		if got := c.saldo(t); got.String() != "1000.00" {
			t.Errorf("saldo = %q, esperado \"1000.00\"", got)
		}
	})

	t.Run("de um ganho debita", func(t *testing.T) {
		c := novoCenario(t, "100.00")
		ctx := context.Background()
		if _, err := c.processar.Execute(ctx, c.comando(wagering.Win, "60.00", "ganho")); err != nil {
			t.Fatalf("ganho: %v", err)
		}
		res, err := c.processar.Execute(ctx, c.reverte(wagering.Rollback, "60.00", "rb", "ganho"))
		if err != nil || res.Status != wagering.Processed {
			t.Fatalf("rollback: %v / %v", err, res.Status)
		}
		if got := c.saldo(t); got.String() != "100.00" {
			t.Errorf("saldo = %q, esperado \"100.00\"", got)
		}
	})

	t.Run("de um estorno debita", func(t *testing.T) {
		c := novoCenario(t, "500.00")
		ctx := context.Background()
		if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "30.00", "aposta")); err != nil {
			t.Fatalf("aposta: %v", err)
		}
		if _, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "30.00", "estorno", "aposta")); err != nil {
			t.Fatalf("estorno: %v", err)
		}
		if got := c.saldo(t); got.String() != "500.00" {
			t.Fatalf("saldo após estorno = %q", got)
		}

		res, err := c.processar.Execute(ctx, c.reverte(wagering.Rollback, "30.00", "rb", "estorno"))
		if err != nil || res.Status != wagering.Processed {
			t.Fatalf("rollback do estorno: %v / %v", err, res.Status)
		}
		if got := c.saldo(t); got.String() != "470.00" {
			t.Errorf("saldo = %q, esperado \"470.00\"", got)
		}
	})
}

// TestEstornoMaisRollbackNaoDevolveDuasVezes é o caso que o índice único do
// banco não cobre: tipos diferentes sobre a mesma referência passariam por ele.
func TestEstornoMaisRollbackNaoDevolveDuasVezes(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "80.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}
	if _, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "80.00", "estorno", "aposta")); err != nil {
		t.Fatalf("estorno: %v", err)
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Fatalf("saldo após estorno = %q", got)
	}

	res, err := c.processar.Execute(ctx, c.reverte(wagering.Rollback, "80.00", "rb", "aposta"))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if res.Status != wagering.Rejected {
		t.Fatalf("estado = %v, esperado REJECTED", res.Status)
	}
	if res.FailureCode != wagering.FailureReferenceAlreadyReversed {
		t.Errorf("código = %v, esperado REFERENCE_ALREADY_REVERSED", res.FailureCode)
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo = %q: o mesmo débito foi devolvido duas vezes", got)
	}
}

func TestSegundoEstornoDaMesmaApostaEhRecusado(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "50.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}
	if _, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "50.00", "estorno-1", "aposta")); err != nil {
		t.Fatalf("primeiro estorno: %v", err)
	}

	res, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "50.00", "estorno-2", "aposta"))
	if err != nil {
		t.Fatalf("segundo estorno: %v", err)
	}
	if res.Status != wagering.Rejected || res.FailureCode != wagering.FailureReferenceAlreadyReversed {
		t.Errorf("resultado = %v/%v", res.Status, res.FailureCode)
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo = %q", got)
	}
}

// TestRollbackSemSaldoTemCodigoProprio cobre a exigência explícita do desafio:
// reversão sem saldo não compartilha o código de aposta sem saldo.
func TestRollbackSemSaldoTemCodigoProprio(t *testing.T) {
	c := novoCenario(t, "0.00")
	ctx := context.Background()

	if _, err := c.processar.Execute(ctx, c.comando(wagering.Win, "100.00", "ganho")); err != nil {
		t.Fatalf("ganho: %v", err)
	}
	if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "80.00", "aposta")); err != nil {
		t.Fatalf("aposta: %v", err)
	}
	if got := c.saldo(t); got.String() != "20.00" {
		t.Fatalf("saldo = %q, esperado \"20.00\"", got)
	}

	// Desfazer o ganho exigiria debitar 100.00 de um saldo de 20.00.
	res, err := c.processar.Execute(ctx, c.reverte(wagering.Rollback, "100.00", "rb", "ganho"))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if res.Status != wagering.Rejected {
		t.Fatalf("estado = %v, esperado REJECTED", res.Status)
	}
	if res.FailureCode != wagering.FailureReversalInsufficientFunds {
		t.Errorf("código = %v, esperado REVERSAL_INSUFFICIENT_FUNDS", res.FailureCode)
	}
	if res.FailureCode == wagering.FailureInsufficientFunds {
		t.Error("reversão sem saldo usou o mesmo código de aposta sem saldo")
	}
	if got := c.saldo(t); got.String() != "20.00" {
		t.Errorf("saldo = %q", got)
	}
}

func TestReferenciaIncompativel(t *testing.T) {
	ctx := context.Background()

	t.Run("valor diferente do referenciado", func(t *testing.T) {
		c := novoCenario(t, "1000.00")
		if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "aposta")); err != nil {
			t.Fatalf("aposta: %v", err)
		}
		res, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "10.00", "estorno", "aposta"))
		if err != nil {
			t.Fatalf("estorno: %v", err)
		}
		if res.FailureCode != wagering.FailureReferenceAmountMismatch {
			t.Errorf("código = %v, esperado REFERENCE_AMOUNT_MISMATCH", res.FailureCode)
		}
	})

	t.Run("rodada diferente", func(t *testing.T) {
		c := novoCenario(t, "1000.00")
		if _, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "25.00", "aposta")); err != nil {
			t.Fatalf("aposta: %v", err)
		}
		cmd := c.reverte(wagering.Refund, "25.00", "estorno", "aposta")
		cmd.RoundID = "outra-rodada"
		res, err := c.processar.Execute(ctx, cmd)
		if err != nil {
			t.Fatalf("estorno: %v", err)
		}
		if res.FailureCode != wagering.FailureReferenceMismatch {
			t.Errorf("código = %v, esperado REFERENCE_MISMATCH", res.FailureCode)
		}
	})

	t.Run("estorno de um ganho não se aplica", func(t *testing.T) {
		c := novoCenario(t, "100.00")
		if _, err := c.processar.Execute(ctx, c.comando(wagering.Win, "25.00", "ganho")); err != nil {
			t.Fatalf("ganho: %v", err)
		}
		res, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "ganho"))
		if err != nil {
			t.Fatalf("estorno: %v", err)
		}
		if res.FailureCode != wagering.FailureReferenceMismatch {
			t.Errorf("código = %v, esperado REFERENCE_MISMATCH: REFUND só devolve aposta", res.FailureCode)
		}
	})

	t.Run("referência que terminou sem sucesso", func(t *testing.T) {
		c := novoCenario(t, "10.00")
		// Aposta recusada por saldo: existe, mas terminou em REJECTED.
		recusada, err := c.processar.Execute(ctx, c.comando(wagering.Bet, "500.00", "recusada"))
		if err != nil {
			t.Fatalf("aposta: %v", err)
		}
		if recusada.Status != wagering.Rejected {
			t.Fatalf("a aposta deveria ter sido recusada, veio %v", recusada.Status)
		}

		res, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "500.00", "estorno", "recusada"))
		if err != nil {
			t.Fatalf("estorno: %v", err)
		}
		if res.FailureCode != wagering.FailureReferenceNotProcessed {
			t.Errorf("código = %v, esperado REFERENCE_NOT_PROCESSED", res.FailureCode)
		}
	})
}

// TestReversaoAntesDaReferenciaFicaPendente cobre o cenário 7 da verificação
// obrigatória: a reversão chega antes da transação que ela desfaz.
func TestReversaoAntesDaReferenciaFicaPendente(t *testing.T) {
	c := novoCenario(t, "1000.00")
	ctx := context.Background()

	res, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "ainda-nao-chegou"))
	if err != nil {
		t.Fatalf("estorno: %v", err)
	}
	if res.Status != wagering.PendingReference {
		t.Fatalf("estado = %v, esperado PENDING_REFERENCE", res.Status)
	}

	tipos := c.eventosDa(t, res.TransactionID)
	if !contem(tipos, "WagerTransactionPendingReference") {
		t.Errorf("eventos = %v, esperado a pendência anunciada", tipos)
	}
	if got := c.saldo(t); got.String() != "1000.00" {
		t.Errorf("saldo = %q: a pendência movimentou a carteira", got)
	}

	// Um reenvio da mesma reversão devolve a pendência, sem duplicar registro.
	repetido, err := c.processar.Execute(ctx, c.reverte(wagering.Refund, "25.00", "estorno", "ainda-nao-chegou"))
	if err != nil {
		t.Fatalf("reenvio: %v", err)
	}
	if !repetido.IdempotentReplay || repetido.TransactionID != res.TransactionID {
		t.Errorf("reenvio = replay %t, transação %v", repetido.IdempotentReplay, repetido.TransactionID)
	}
}

func TestReversaoRejeitaComandoInvalido(t *testing.T) {
	c := novoCenario(t, "100.00")
	ctx := context.Background()

	t.Run("sem referência", func(t *testing.T) {
		cmd := c.comando(wagering.Refund, "10.00", "sem-ref")
		if _, err := c.processar.Execute(ctx, cmd); !errors.Is(err, app.ErrInvalidCommand) {
			t.Errorf("devolveu %v, esperado ErrInvalidCommand", err)
		}
	})

	t.Run("referenciando a si mesma", func(t *testing.T) {
		cmd := c.reverte(wagering.Refund, "10.00", "auto", "auto")
		if _, err := c.processar.Execute(ctx, cmd); !errors.Is(err, app.ErrInvalidCommand) {
			t.Errorf("devolveu %v, esperado ErrInvalidCommand", err)
		}
	})

	t.Run("operação comum com referência", func(t *testing.T) {
		cmd := c.comando(wagering.Bet, "10.00", "com-ref")
		cmd.ReferenceExternalTransactionID = "qualquer"
		if _, err := c.processar.Execute(ctx, cmd); !errors.Is(err, app.ErrInvalidCommand) {
			t.Errorf("devolveu %v, esperado ErrInvalidCommand", err)
		}
	})
}
