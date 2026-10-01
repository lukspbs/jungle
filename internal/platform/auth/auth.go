// Package auth valida tokens de um IdP externo e extrai a identidade.
//
// O serviço não emite tokens nem guarda senhas: ele confia num emissor OIDC
// externo e verifica assinatura, emissor, audiência e validade a cada
// requisição. As chaves públicas vêm do JWKS do emissor e são renovadas pelo
// cliente OIDC, de modo que a rotação de chaves no IdP não exige reinício aqui.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/lukspbs/jungle/internal/platform/config"
)

// Erros de autenticação e autorização.
var (
	// ErrUnauthenticated indica token ausente, malformado, expirado ou com
	// assinatura inválida.
	ErrUnauthenticated = errors.New("auth: credencial ausente ou inválida")

	// ErrForbidden indica identidade autenticada sem permissão para a operação.
	ErrForbidden = errors.New("auth: identidade sem permissão para esta operação")
)

// Papéis do realm.
const (
	// RoleWalletAdmin autoriza as operações de carteira. É do serviço interno:
	// abertura, consulta e reconciliação não são expostas a provedores.
	RoleWalletAdmin = "wallet-admin"

	// RoleWagerProvider autoriza o envio e a consulta de operações do próprio
	// provedor.
	RoleWagerProvider = "wager-provider"
)

// Identity é a identidade autenticada de uma requisição.
type Identity struct {
	// Subject é o identificador do sujeito no IdP.
	Subject string

	// ClientID é o cliente que obteve o token.
	ClientID string

	// ProviderID é o provedor que esta identidade representa. Vazio para o
	// serviço interno, que não é um provedor.
	//
	// É este campo, e não o corpo da requisição, que determina o provedor
	// autorizado: um provedor não consegue agir em nome de outro mudando o
	// JSON que envia.
	ProviderID string

	Roles []string
}

// HasRole informa se a identidade tem o papel.
func (i Identity) HasRole(role string) bool {
	for _, r := range i.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsWalletAdmin informa se a identidade opera carteiras.
func (i Identity) IsWalletAdmin() bool { return i.HasRole(RoleWalletAdmin) }

// IsProvider informa se a identidade representa um provedor.
func (i Identity) IsProvider() bool {
	return i.ProviderID != "" && i.HasRole(RoleWagerProvider)
}

// claims é a parte do token que interessa.
type claims struct {
	Subject     string `json:"sub"`
	ClientID    string `json:"azp"`
	ProviderID  string `json:"provider_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Verifier valida tokens contra o emissor configurado.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier descobre o emissor e monta o validador.
//
// A descoberta acontece na construção: um emissor inacessível impede a
// aplicação de subir, em vez de deixá-la aceitar requisições que não consegue
// autenticar.
func NewVerifier(ctx context.Context, cfg config.Auth) (*Verifier, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("auth: falha ao descobrir o emissor %s: %w", cfg.IssuerURL, err)
	}

	return &Verifier{
		verifier: provider.Verifier(&oidc.Config{
			// A audiência é verificada: um token emitido para outro serviço do
			// mesmo realm não serve aqui.
			ClientID: cfg.Audience,
			// Tokens de client_credentials não carregam nonce nem at_hash.
			SkipClientIDCheck: false,
		}),
	}, nil
}

// Verify valida o token e extrai a identidade.
func (v *Verifier) Verify(ctx context.Context, raw string) (Identity, error) {
	if raw == "" {
		return Identity{}, fmt.Errorf("%w: token ausente", ErrUnauthenticated)
	}

	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		// A mensagem do verificador descreve a causa (expirado, assinatura
		// inválida, audiência errada) e é útil no log, mas não volta ao cliente.
		return Identity{}, fmt.Errorf("%w: %s", ErrUnauthenticated, err)
	}

	var c claims
	if err := token.Claims(&c); err != nil {
		return Identity{}, fmt.Errorf("%w: claims ilegíveis", ErrUnauthenticated)
	}

	return Identity{
		Subject:    c.Subject,
		ClientID:   c.ClientID,
		ProviderID: c.ProviderID,
		Roles:      c.RealmAccess.Roles,
	}, nil
}

// BearerToken extrai o token do cabeçalho Authorization.
func BearerToken(header string) string {
	const prefixo = "Bearer "
	if len(header) <= len(prefixo) || !strings.EqualFold(header[:len(prefixo)], prefixo) {
		return ""
	}
	return strings.TrimSpace(header[len(prefixo):])
}
