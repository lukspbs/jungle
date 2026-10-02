// Package authtest obtém tokens de um Keycloak real para os testes.
//
// Nada aqui forja token nem substitui o IdP: os testes exercitam a integração
// de verdade, pedindo credenciais ao emissor pelo fluxo client_credentials e
// deixando o serviço validá-las como validaria em produção.
package authtest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lukspbs/jungle/internal/platform/auth"
	"github.com/lukspbs/jungle/internal/platform/config"
)

// EnvIssuer aponta para o realm do Keycloak de teste.
const EnvIssuer = "TEST_AUTH_ISSUER_URL"

// Credenciais provisionadas pelo realm importado. São de ambiente local e
// estão versionadas junto com o realm, de propósito: um avaliador precisa
// conseguir subir e autenticar sem receber segredo por fora.
const (
	ClientInternal  = "jungle-internal"
	SecretInternal  = "internal-secret-local"
	ClientProviderA = "provider-a"
	SecretProviderA = "provider-a-secret-local"
	ClientProviderB = "provider-b"
	SecretProviderB = "provider-b-secret-local"
	Audience        = "jungle-api"

	// O client efêmero tem lifespan de 1s no realm. Existe só para que um
	// teste consiga um token legítimo e já vencido.
	ClientEfemero = "provider-efemero"
	SecretEfemero = "provider-efemero-secret-local"
)

// Config devolve a configuração de autenticação, pulando o teste sem o IdP.
func Config(t *testing.T) config.Auth {
	t.Helper()
	issuer := os.Getenv(EnvIssuer)
	if issuer == "" {
		t.Skipf("%s não definida: teste de autenticação pulado", EnvIssuer)
	}
	return config.Auth{IssuerURL: issuer, Audience: Audience}
}

// Verifier monta o validador contra o emissor de teste.
func Verifier(t *testing.T) *auth.Verifier {
	t.Helper()
	cfg := Config(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	v, err := auth.NewVerifier(ctx, cfg)
	if err != nil {
		t.Fatalf("falha ao descobrir o emissor %s: %v", cfg.IssuerURL, err)
	}
	return v
}

// Token obtém um access token por client_credentials.
func Token(t *testing.T, clientID, secret string) string {
	t.Helper()
	cfg := Config(t)

	corpo := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.IssuerURL+"/protocol/openid-connect/token", strings.NewReader(corpo.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("falha ao pedir token para %s: %v", clientID, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("o emissor recusou as credenciais de %s: HTTP %d", clientID, res.StatusCode)
	}

	var resposta struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resposta); err != nil {
		t.Fatalf("resposta do emissor ilegível: %v", err)
	}
	if resposta.AccessToken == "" {
		t.Fatal("o emissor devolveu token vazio")
	}
	return resposta.AccessToken
}

// Internal devolve o token do serviço interno.
func Internal(t *testing.T) string { return Token(t, ClientInternal, SecretInternal) }

// ProviderA devolve o token do provedor A.
func ProviderA(t *testing.T) string { return Token(t, ClientProviderA, SecretProviderA) }

// ProviderB devolve o token do provedor B, usado para provar o isolamento.
func ProviderB(t *testing.T) string { return Token(t, ClientProviderB, SecretProviderB) }

// Expirado devolve um token emitido de verdade e já fora da validade.
//
// O token vem do Keycloak, por um client cujo lifespan é de um segundo. Forjar
// um JWT com exp no passado não serviria: a assinatura falharia antes de a
// validade ser olhada, e o teste passaria pelo motivo errado — provando que
// assinatura inválida é recusada, coisa que outro teste já prova.
func Expirado(t *testing.T) string {
	t.Helper()
	token := Token(t, ClientEfemero, SecretEfemero)

	// Um segundo além do exp. Parar no limite exato deixaria o resultado na
	// mão do arredondamento do relógio.
	if espera := time.Until(expiracao(t, token).Add(time.Second)); espera > 0 {
		time.Sleep(espera)
	}
	return token
}

// expiracao lê o claim exp sem verificar nada.
//
// O teste precisa do exp para saber quanto esperar, não para confiar no token
// — quem valida é o serviço, que é justamente o que está sob teste.
func expiracao(t *testing.T, token string) time.Time {
	t.Helper()

	partes := strings.Split(token, ".")
	if len(partes) != 3 {
		t.Fatalf("o emissor devolveu algo que não é um JWT de três partes")
	}
	corpo, err := base64.RawURLEncoding.DecodeString(partes[1])
	if err != nil {
		t.Fatalf("corpo do token ilegível: %v", err)
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(corpo, &claims); err != nil {
		t.Fatalf("claims ilegíveis: %v", err)
	}
	if claims.Exp == 0 {
		t.Fatal("o token não traz exp: não há validade para vencer")
	}
	return time.Unix(claims.Exp, 0)
}

// Bearer formata o cabeçalho.
func Bearer(token string) string { return fmt.Sprintf("Bearer %s", token) }
