package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/lukspbs/jungle/internal/app"
	"github.com/lukspbs/jungle/internal/domain/wagering"
)

// maxBodyBytes limita o corpo aceito. Os corpos deste contrato são pequenos; o
// teto evita que uma requisição malformada consuma memória sem limite.
const maxBodyBytes = 64 << 10

// defaultLedgerLimit e maxLedgerLimit governam a paginação do extrato.
const (
	defaultLedgerLimit = 50
	maxLedgerLimit     = 200
)

// Handlers reúne os endpoints da API.
type Handlers struct {
	openWallet   *app.OpenWallet
	processWager *app.ProcessWager
	queries      *app.Queries
	readiness    ReadinessChecker
}

// ReadinessChecker informa se as dependências externas respondem.
type ReadinessChecker interface {
	Check(ctx context.Context) error
}

// NewHandlers monta os endpoints.
func NewHandlers(
	openWallet *app.OpenWallet, processWager *app.ProcessWager,
	queries *app.Queries, readiness ReadinessChecker,
) *Handlers {
	return &Handlers{
		openWallet: openWallet, processWager: processWager,
		queries: queries, readiness: readiness,
	}
}

// OpenWallet responde POST /wallets.
func (h *Handlers) OpenWallet(w http.ResponseWriter, r *http.Request) {
	if !requireWalletAdmin(w, r) {
		return
	}

	var req openWalletRequest
	if !decode(w, r, &req) {
		return
	}

	playerID, err := parseUUID("playerId", req.PlayerID)
	if err != nil {
		writeError(w, r, err)
		return
	}

	res, err := h.openWallet.Execute(r.Context(), app.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: req.InitialBalance,
		CorrelationID:  correlationID(r),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, walletOf(res.Wallet))
}

// ProcessWager responde POST /wagering/transactions.
//
// O código de resposta distingue os desfechos: 200 para operação concluída,
// 202 para a que ficou aguardando referência, 422 para a recusada por regra de
// negócio. Um replay devolve o mesmo código do processamento original.
func (h *Handlers) ProcessWager(w http.ResponseWriter, r *http.Request) {
	// A chave vem do cabeçalho e é usada como veio. O servidor não a calcula
	// nem a substitui: o cliente pode montá-la como provider:externalId, mas
	// quem decide é ele.
	// O provedor vem do token. O corpo pode repeti-lo, mas não pode contrariá-lo.
	providerID, ok := requireProvider(w, r)
	if !ok {
		return
	}

	chave := r.Header.Get("Idempotency-Key")
	if chave == "" {
		writeProblem(w, r, http.StatusBadRequest, codeMissingIdempotency,
			"o cabeçalho Idempotency-Key é obrigatório")
		return
	}

	var req wagerRequest
	if !decode(w, r, &req) {
		return
	}

	if req.ProviderID != "" && req.ProviderID != providerID {
		writeProblem(w, r, http.StatusForbidden, codeForbidden,
			"providerId do corpo difere da identidade autenticada")
		return
	}
	req.ProviderID = providerID

	cmd, err := req.toCommand(chave, correlationID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}

	res, err := h.processWager.Execute(r.Context(), cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}

	switch res.Status {
	case wagering.PendingReference:
		writeJSON(w, http.StatusAccepted, wagerOf(res))
	case wagering.Rejected:
		writeJSON(w, http.StatusUnprocessableEntity, wagerOf(res))
	default:
		writeJSON(w, http.StatusOK, wagerOf(res))
	}
}

func (req wagerRequest) toCommand(chave, correlationID string) (app.ProcessWagerCommand, error) {
	playerID, err := parseUUID("playerId", req.PlayerID)
	if err != nil {
		return app.ProcessWagerCommand{}, err
	}
	walletID, err := parseUUID("walletId", req.WalletID)
	if err != nil {
		return app.ProcessWagerCommand{}, err
	}
	// ParseExternalKind é o ponto que recusa OPENING vindo de fora.
	kind, err := wagering.ParseExternalKind(req.Kind)
	if err != nil {
		return app.ProcessWagerCommand{}, err
	}

	return app.ProcessWagerCommand{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 chave,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           kind,
		Money:                          req.Money,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
		CorrelationID:                  correlationID,
	}, nil
}

// GetWallet responde GET /wallets/{walletId}.
func (h *Handlers) GetWallet(w http.ResponseWriter, r *http.Request) {
	if !requireWalletAdmin(w, r) {
		return
	}
	walletID, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	carteira, err := h.queries.Wallet(r.Context(), walletID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, walletOf(carteira))
}

// GetLedger responde GET /wallets/{walletId}/ledger.
func (h *Handlers) GetLedger(w http.ResponseWriter, r *http.Request) {
	if !requireWalletAdmin(w, r) {
		return
	}
	walletID, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	limite, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	pagina, err := h.queries.Ledger(r.Context(), walletID, cursor, limite)
	if err != nil {
		writeError(w, r, err)
		return
	}

	resposta := ledgerPageResponse{
		WalletID:   walletID,
		Entries:    make([]ledgerEntryResponse, 0, len(pagina.Entries)),
		NextCursor: encodeCursor(pagina.NextCursor),
	}
	for _, e := range pagina.Entries {
		resposta.Entries = append(resposta.Entries, ledgerEntryResponse{
			ID: e.ID(), TransactionID: e.TransactionID(),
			Direction: e.Direction().String(), Money: e.Amount(),
			BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(),
			CreatedAt: rfc3339(e.CreatedAt()),
		})
	}
	writeJSON(w, http.StatusOK, resposta)
}

// GetTransaction responde GET /wagering/transactions/{transactionId}.
func (h *Handlers) GetTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("transactionId", r.PathValue("transactionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	tx, err := h.queries.Transaction(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// A autorização acontece depois da leitura porque o provedor dono só é
	// conhecido ao ler a transação. O identificador interno é opaco, então
	// adivinhá-lo para sondar a existência alheia não é um caminho prático — e
	// ainda assim a resposta é 404, não 403.
	if !authorizeProviderScope(w, r, tx.ProviderID()) {
		return
	}
	writeJSON(w, http.StatusOK, transactionOf(tx))
}

// GetProviderTransaction responde
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
func (h *Handlers) GetProviderTransaction(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	externalID := r.PathValue("externalTransactionId")
	if providerID == "" || externalID == "" {
		writeProblem(w, r, http.StatusBadRequest, codeInvalidRequest,
			"providerId e externalTransactionId são obrigatórios")
		return
	}

	if !authorizeProviderScope(w, r, providerID) {
		return
	}

	tx, err := h.queries.ProviderTransaction(r.Context(), providerID, externalID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionOf(tx))
}

// Reconcile responde POST /wallets/{walletId}/reconciliation.
func (h *Handlers) Reconcile(w http.ResponseWriter, r *http.Request) {
	if !requireWalletAdmin(w, r) {
		return
	}
	walletID, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.queries.Reconcile(r.Context(), walletID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID:          res.WalletID,
		StoredBalance:     res.StoredBalance,
		CalculatedBalance: res.CalculatedBalance,
		Difference:        res.Difference,
		Consistent:        res.Consistent,
		CheckedEntries:    res.CheckedEntries,
	})
}

// Live responde GET /health/live: o processo está de pé.
func (h *Handlers) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

// Ready responde GET /health/ready: as dependências respondem.
//
// Liveness e readiness são separados de propósito: um processo vivo com banco
// fora do ar deve parar de receber tráfego, não ser reiniciado.
func (h *Handlers) Ready(w http.ResponseWriter, r *http.Request) {
	if err := h.readiness.Check(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unready", "reason": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func parseUUID(campo, valor string) (uuid.UUID, error) {
	if valor == "" {
		return uuid.Nil, fmt.Errorf("%w: %s é obrigatório", app.ErrInvalidCommand, campo)
	}
	id, err := uuid.Parse(valor)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s não é um UUID válido", app.ErrInvalidCommand, campo)
	}
	return id, nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLedgerLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%w: limit precisa ser um inteiro positivo", app.ErrInvalidCommand)
	}
	if n > maxLedgerLimit {
		return maxLedgerLimit, nil
	}
	return n, nil
}

// decode lê o corpo e responde 400 em caso de problema, devolvendo false.
func decode(w http.ResponseWriter, r *http.Request, destino any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(destino); err != nil {
		writeError(w, r, fmt.Errorf("%w: %s", app.ErrInvalidCommand, mensagemDeDecodificacao(err)))
		return false
	}
	// Um corpo com mais de um documento JSON é entrada malformada.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeError(w, r, fmt.Errorf("%w: corpo com conteúdo extra após o JSON", app.ErrInvalidCommand))
		return false
	}
	return true
}

// mensagemDeDecodificacao devolve o erro de forma, mas preserva os erros de
// domínio que o Money levanta ao desserializar — eles são mais informativos.
func mensagemDeDecodificacao(err error) string {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return "corpo maior que o permitido"
	}
	return err.Error()
}
