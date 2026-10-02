package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lukspbs/jungle/internal/adapter/postgres"
	"github.com/lukspbs/jungle/internal/domain/events"
	"github.com/lukspbs/jungle/internal/domain/wagering"
	"github.com/lukspbs/jungle/internal/platform/logging"
	"github.com/lukspbs/jungle/internal/platform/metrics"
)

// ReferenceWorker retoma as operações que ficaram aguardando uma referência.
//
// A retomada é durável e não depende de quem registrou a pendência: o estado
// vive no banco, a reivindicação usa SKIP LOCKED, e qualquer instância assume
// qualquer pendência. Reiniciar todos os processos não perde nada — a varredura
// seguinte encontra as pendências onde elas estavam.
type ReferenceWorker struct {
	processar *ProcessWager
	store     *postgres.Store
	clock     Clock
	logger    *slog.Logger
	metrics   *metrics.Metrics
	policy    ReferencePolicy
	batchSize int
	interval  time.Duration
}

// NewReferenceWorker monta o worker.
func NewReferenceWorker(
	processar *ProcessWager, store *postgres.Store, clock Clock, logger *slog.Logger,
	m *metrics.Metrics, policy ReferencePolicy, batchSize int, interval time.Duration,
) *ReferenceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.New()
	}
	return &ReferenceWorker{
		processar: processar, store: store, clock: clock,
		logger:  logger.With(slog.String("component", "reference-worker")),
		metrics: m,
		policy:  policy, batchSize: batchSize, interval: interval,
	}
}

// SweepResult resume uma varredura.
type SweepResult struct {
	Claimed   int
	Processed int
	Rejected  int
	Postponed int
}

// Run varre periodicamente até o contexto ser cancelado.
//
// O cancelamento interrompe a espera entre varreduras, mas uma varredura em
// andamento termina: ela está dentro de uma transação, e abandoná-la no meio só
// adiaria o trabalho para a próxima.
func (w *ReferenceWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := w.Sweep(ctx); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				// Uma varredura que falha não derruba o worker: a próxima tenta
				// de novo, e as pendências continuam no banco. Mas ela precisa
				// aparecer, ou um worker quebrado fica indistinguível de um
				// worker sem trabalho.
				w.logger.ErrorContext(ctx, "varredura de referências falhou",
					slog.String("error", err.Error()))
				continue
			}
		}
	}
}

// Sweep executa uma varredura.
func (w *ReferenceWorker) Sweep(ctx context.Context) (SweepResult, error) {
	agora := w.clock.Now()

	// O prazo provisório segura a pendência durante o processamento, para que
	// outra instância não a reivindique ao mesmo tempo. O prazo definitivo é
	// gravado depois, conforme o desfecho.
	provisorio := agora.Add(w.policy.MaxBackoff)

	pendentes, err := w.store.Read().Transactions.ClaimPendingReferences(
		ctx, w.batchSize, agora, provisorio)
	if err != nil {
		return SweepResult{}, err
	}

	resultado := SweepResult{Claimed: len(pendentes)}
	for _, pendente := range pendentes {
		desfecho, err := w.resume(ctx, pendente, agora)
		if err != nil {
			// Falhar numa pendência não aborta a varredura: as outras seguem,
			// e esta volta na próxima.
			w.logger.ErrorContext(ctx, "pendência não pôde ser retomada",
				slog.String(logging.FieldTransactionID, pendente.ID().String()),
				slog.String(logging.FieldProviderID, pendente.ProviderID()),
				slog.String("error", err.Error()))
			continue
		}
		w.metrics.ObserveReferenceOutcome(strings.ToLower(desfecho.String()))
		w.logger.InfoContext(ctx, "pendência retomada",
			slog.String(logging.FieldTransactionID, pendente.ID().String()),
			slog.String(logging.FieldProviderID, pendente.ProviderID()),
			slog.String(logging.FieldStatus, desfecho.String()))
		switch desfecho {
		case wagering.Processed:
			resultado.Processed++
		case wagering.Rejected:
			resultado.Rejected++
		default:
			resultado.Postponed++
		}
	}
	return resultado, nil
}

// resume tenta concluir uma pendência.
func (w *ReferenceWorker) resume(
	ctx context.Context, pendente *wagering.WagerTransaction, agora time.Time,
) (wagering.Status, error) {
	var desfecho wagering.Status

	err := w.store.InTx(ctx, func(ctx context.Context, r *postgres.Repositories) error {
		carteira, err := r.Wallets.LockForUpdate(ctx, pendente.WalletID())
		if err != nil {
			return err
		}

		// Reler sob o lock: outra instância pode ter concluído esta pendência
		// entre a reivindicação e agora.
		tx, err := r.Transactions.FindByID(ctx, pendente.ID())
		if err != nil {
			return err
		}
		if tx.Status().IsTerminal() {
			desfecho = tx.Status()
			return nil
		}

		cmd := commandFrom(tx)
		ref, direcao, decisao, code, err := w.processar.resolveReference(ctx, r, cmd, carteira)
		if err != nil {
			return err
		}

		var out ProcessWagerResult
		switch decisao {
		case resolutionReject:
			if err := w.processar.rejectWith(ctx, r, tx, code, agora, cmd.CorrelationID,
				&out, r.Transactions.Update); err != nil {
				return err
			}
			desfecho = wagering.Rejected
			return nil

		case resolutionWait:
			return w.postpone(ctx, r, tx, cmd, agora, &desfecho)
		}

		// A referência chegou e serve: aplicar.
		if err := tx.ResolveReference(ref.ID(), agora); err != nil {
			return err
		}
		versaoAnterior := carteira.Version()

		entry, movErr := w.processar.move(carteira, tx, cmd, direcao, agora)
		if movErr != nil {
			codigo, recusa := classifyMovement(movErr, cmd.Kind)
			if !recusa {
				return movErr
			}
			if err := w.processar.rejectWith(ctx, r, tx, codigo, agora, cmd.CorrelationID,
				&out, r.Transactions.Update); err != nil {
				return err
			}
			desfecho = wagering.Rejected
			return nil
		}

		if err := tx.MarkProcessed(carteira.Balance(), agora); err != nil {
			return err
		}
		if err := r.Transactions.Update(ctx, tx); err != nil {
			return err
		}

		emitidos := []events.Payload{processedPayload(tx, carteira, cmd)}

		// move devolve lançamento nulo para tipo que não movimenta saldo. Hoje
		// nenhum deles chega aqui — só reversões ficam pendentes de referência,
		// e todas movimentam — mas repito a mesma guarda de processWithin em
		// vez de depender dessa coincidência: o dia que um tipo reversível sem
		// movimentação existir, isto seria um nil dentro de uma transação
		// financeira.
		if entry != nil {
			if err := r.Ledger.Insert(ctx, entry); err != nil {
				return err
			}
			if err := r.Wallets.UpdateBalance(ctx, carteira, versaoAnterior); err != nil {
				return err
			}
			emitidos = append(emitidos, balanceChangedPayload(tx, carteira, entry, agora))
		}
		if err := w.processar.emit(ctx, r, emitidos, cmd.CorrelationID, tx.ID().String(), agora); err != nil {
			return err
		}

		desfecho = wagering.Processed
		return nil
	})

	return desfecho, err
}

// postpone agenda nova tentativa, ou encerra a espera quando o prazo ou as
// tentativas se esgotam.
//
// O encerramento é uma recusa com código próprio e evento, não um
// desaparecimento silencioso: o provedor precisa saber que a reversão não vai
// mais acontecer, e a decisão precisa ficar auditável.
func (w *ReferenceWorker) postpone(
	ctx context.Context, r *postgres.Repositories, tx *wagering.WagerTransaction,
	cmd ProcessWagerCommand, agora time.Time, desfecho *wagering.Status,
) error {
	tentativas, expiraEm, err := r.Transactions.ReferenceExpiry(ctx, tx.ID())
	if err != nil {
		return err
	}

	esgotou := tentativas >= w.policy.MaxAttempts || !agora.Before(expiraEm)
	if esgotou {
		var out ProcessWagerResult
		if err := w.processar.rejectWith(ctx, r, tx, wagering.FailureReferenceNotFound,
			agora, cmd.CorrelationID, &out, r.Transactions.Update); err != nil {
			return err
		}
		*desfecho = wagering.Rejected
		return nil
	}

	proxima := agora.Add(w.policy.backoffFor(tentativas))
	if err := r.Transactions.ScheduleReferenceRetry(ctx, tx.ID(), proxima, expiraEm); err != nil {
		return err
	}
	*desfecho = wagering.PendingReference
	return nil
}

// commandFrom reconstrói o comando a partir da transação persistida.
//
// A retomada precisa dos mesmos campos de negócio da chegada original, e eles
// estão todos na transação — foi para isso que ela os gravou.
func commandFrom(tx *wagering.WagerTransaction) ProcessWagerCommand {
	return ProcessWagerCommand{
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		IdempotencyKey:                 tx.IdempotencyKey(),
		PlayerID:                       tx.PlayerID(),
		WalletID:                       tx.WalletID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           tx.Kind(),
		Money:                          tx.Amount(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		// A retomada é um ato do worker, não da requisição original: o
		// correlationId identifica a transação para que os eventos dela
		// continuem rastreáveis.
		CorrelationID: fmt.Sprintf("reference-worker:%s", tx.ID()),
	}
}
