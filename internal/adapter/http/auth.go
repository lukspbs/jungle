package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lukspbs/jungle/internal/platform/auth"
)

const identityKey contextKey = "identity"

// caminhosPublicos dispensam autenticação.
//
// Só os health checks. Um verificador de readiness de orquestrador não tem
// credencial, e exigir uma transformaria indisponibilidade do IdP em
// indisponibilidade aparente do serviço.
var caminhosPublicos = map[string]bool{
	"/health/live":  true,
	"/health/ready": true,
}

// Authenticate valida o token e injeta a identidade no contexto.
//
// Tudo que não é health check exige credencial válida. Não há modo de contorno,
// nem variável que desligue a verificação: um interruptor desses acaba ligado
// onde não devia.
func Authenticate(verifier *auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if caminhosPublicos[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			identidade, err := verifier.Verify(r.Context(), auth.BearerToken(r.Header.Get("Authorization")))
			if err != nil {
				// O motivo exato fica no log; o cliente recebe só que a
				// credencial não serve. Detalhar ajudaria quem está sondando.
				w.Header().Set("WWW-Authenticate", `Bearer realm="jungle"`)
				writeProblem(w, r, http.StatusUnauthorized, codeUnauthenticated,
					"credencial ausente ou inválida")
				return
			}

			next.ServeHTTP(w, r.WithContext(
				context.WithValue(r.Context(), identityKey, identidade)))
		})
	}
}

// identityOf devolve a identidade da requisição.
func identityOf(r *http.Request) (auth.Identity, bool) {
	id, ok := r.Context().Value(identityKey).(auth.Identity)
	return id, ok
}

// requireWalletAdmin autoriza as operações de carteira.
//
// Abertura, consulta, extrato e reconciliação são do serviço interno. Um
// provedor autenticado não as alcança.
func requireWalletAdmin(w http.ResponseWriter, r *http.Request) bool {
	identidade, ok := identityOf(r)
	if !ok || !identidade.IsWalletAdmin() {
		writeProblem(w, r, http.StatusForbidden, codeForbidden,
			"esta operação é restrita ao serviço interno")
		return false
	}
	return true
}

// requireProvider autoriza um provedor e devolve o provedor que ele representa.
//
// O provedor vem do token, nunca do corpo ou do caminho. É isso que impede uma
// identidade de agir em nome de outra apenas mudando o que envia.
func requireProvider(w http.ResponseWriter, r *http.Request) (string, bool) {
	identidade, ok := identityOf(r)
	if !ok || !identidade.IsProvider() {
		writeProblem(w, r, http.StatusForbidden, codeForbidden,
			"esta operação exige uma identidade de provedor")
		return "", false
	}
	return identidade.ProviderID, true
}

// authorizeProviderScope confere que o provedor pedido é o do token.
//
// Devolver 404 em vez de 403 quando o provedor difere é deliberado: responder
// "existe, mas não é seu" confirmaria a existência de uma operação alheia.
func authorizeProviderScope(w http.ResponseWriter, r *http.Request, pedido string) bool {
	identidade, ok := identityOf(r)
	if !ok {
		writeProblem(w, r, http.StatusForbidden, codeForbidden, "identidade ausente")
		return false
	}
	if identidade.IsWalletAdmin() {
		// O serviço interno enxerga qualquer provedor: ele é quem reconcilia.
		return true
	}
	if !identidade.IsProvider() || !strings.EqualFold(identidade.ProviderID, pedido) {
		writeProblem(w, r, http.StatusNotFound, codeNotFound, "operação não encontrada")
		return false
	}
	return true
}

// errorStatusFor acrescenta os erros de autorização ao mapeamento.
func authStatusFor(err error) (int, string, bool) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		return http.StatusUnauthorized, codeUnauthenticated, true
	case errors.Is(err, auth.ErrForbidden):
		return http.StatusForbidden, codeForbidden, true
	default:
		return 0, "", false
	}
}
