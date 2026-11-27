package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password should verify")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("wrong password must not verify")
	}
}

func TestPATGenerationAndVerification(t *testing.T) {
	full, tokenID, prefix, hash, err := GeneratePAT()
	if err != nil {
		t.Fatal(err)
	}
	parsedID, secret, ok := ParsePAT(full)
	if !ok || parsedID != tokenID || secret == "" {
		t.Fatalf("failed to parse generated PAT: id=%q ok=%v", parsedID, ok)
	}
	if !strings.HasPrefix(full, "crp_"+tokenID+"_") {
		t.Fatalf("unexpected PAT format %q", full)
	}
	if !strings.HasPrefix(full, "crp_"+tokenID+"_") || !strings.HasSuffix(prefix, "...") {
		t.Fatalf("unexpected prefix %q", prefix)
	}
	if !VerifyPATSecret(hash, secret) {
		t.Fatal("PAT secret should verify")
	}
	if VerifyPATSecret(hash, secret+"x") {
		t.Fatal("wrong PAT secret must not verify")
	}
}

func TestJWTSignAndVerify(t *testing.T) {
	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), "crproxy", "crproxy", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, claims, err := signer.Issue("usr_1", "admin", "basic", []Access{
		{Type: "repository", Name: "demo/app", Actions: []string{"pull", "push"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := signer.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Subject != claims.Subject || len(verified.Access) != 1 || verified.Access[0].Name != "demo/app" {
		t.Fatalf("unexpected claims: %#v", verified)
	}
	if _, err := signer.Verify(token + "x"); err == nil {
		t.Fatal("tampered token must be rejected")
	}
	other, err := NewSigner([]byte("abcdef0123456789abcdef0123456789"), "crproxy", "crproxy", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Verify(token); err == nil {
		t.Fatal("token signed with another key must be rejected")
	}
}

func TestJWTRejectsNoneAlgorithm(t *testing.T) {
	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), "crproxy", "crproxy", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.e30."
	if _, err := signer.Verify(token); err == nil {
		t.Fatal("none algorithm must be rejected")
	}
}

func TestBearerAuthorizationSchemeIsCaseInsensitive(t *testing.T) {
	for _, header := range []string{"Bearer token-value", "bearer   token-value", "  BEARER\ttoken-value  "} {
		token, ok := bearerTokenFromHeader(header)
		if !ok || token != "token-value" {
			t.Errorf("bearerTokenFromHeader(%q) = %q, %v", header, token, ok)
		}
	}
	for _, header := range []string{"", "Bearer", "Bearer ", "Basic token-value", "Token token-value"} {
		if token, ok := bearerTokenFromHeader(header); ok {
			t.Errorf("bearerTokenFromHeader(%q) should fail, got %q", header, token)
		}
	}
}

func TestAuthenticatorApplyPolicyHotUpdatesChallengeAndTokens(t *testing.T) {
	signer, err := NewSigner([]byte("0123456789abcdef0123456789abcdef"), "crproxy", "old-service", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &Authenticator{
		Signer:        signer,
		Realm:         "http://old.example",
		Service:       "old-service",
		ChallengeMode: "bearer",
	}
	if challenge := authenticator.Challenge("demo/app", []string{"pull"}); !strings.Contains(challenge, `realm="http://old.example/v2/token"`) {
		t.Fatalf("unexpected initial challenge %q", challenge)
	}
	oldToken, oldClaims, err := authenticator.IssueToken(&Identity{UserID: "usr_1", Role: "admin", Scopes: []string{"pull"}}, []ScopeRequest{{Repository: "demo/app", Actions: []string{"pull"}}})
	if err != nil {
		t.Fatal(err)
	}
	if oldClaims.ExpiresAt-oldClaims.IssuedAt != 300 {
		t.Fatalf("unexpected initial token ttl: %d", oldClaims.ExpiresAt-oldClaims.IssuedAt)
	}
	if err := authenticator.ApplyPolicy(RegistryPolicy{
		Realm:          "https://new.example",
		Service:        "new-service",
		ChallengeMode:  "bearer",
		AllowAnonymous: true,
		TokenTTL:       2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if challenge := authenticator.Challenge("demo/app", []string{"pull"}); !strings.Contains(challenge, `realm="https://new.example/v2/token"`) || !strings.Contains(challenge, `service="new-service"`) {
		t.Fatalf("challenge was not hot-updated: %q", challenge)
	}
	request := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	identity, err := authenticator.AuthenticateBase(request.Context(), request)
	if err != nil || identity == nil || !identity.Anonymous {
		t.Fatalf("anonymous policy was not hot-updated: identity=%+v err=%v", identity, err)
	}
	identity, err = authenticator.Authorize(request.Context(), request, "demo/app", "pull")
	if err != nil || identity == nil || !identity.Anonymous {
		t.Fatalf("anonymous pull should be allowed: identity=%+v err=%v", identity, err)
	}
	if _, err := authenticator.Authorize(request.Context(), request, "demo/app", "push"); err == nil {
		t.Fatal("anonymous push must remain authenticated")
	}
	if _, err := authenticator.Authorize(request.Context(), request, "", "catalog"); err == nil {
		t.Fatal("anonymous catalog access must remain authenticated")
	}
	newToken, newClaims, err := authenticator.IssueToken(&Identity{UserID: "usr_1", Role: "admin", Scopes: []string{"pull"}}, []ScopeRequest{{Repository: "demo/app", Actions: []string{"pull"}}})
	if err != nil {
		t.Fatal(err)
	}
	if newClaims.ExpiresAt-newClaims.IssuedAt != 120 || newClaims.Audience != "new-service" {
		t.Fatalf("token policy was not hot-updated: %+v", newClaims)
	}
	if _, err := signer.Verify(oldToken); err == nil {
		t.Fatal("token issued for the previous audience must be invalid after service change")
	}
	if _, err := signer.Verify(newToken); err != nil {
		t.Fatalf("new token should verify: %v", err)
	}
	if err := authenticator.ApplyPolicy(RegistryPolicy{
		Realm:            "https://new.example",
		Service:          "new-service",
		ChallengeMode:    "bearer",
		AllowAnonymous:   true,
		AnonymousActions: []string{"pull", "push"},
		TokenTTL:         2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authorize(request.Context(), request, "demo/app", "push"); err != nil {
		t.Fatalf("anonymous push should be allowed when configured: %v", err)
	}
	if _, err := authenticator.Authorize(request.Context(), request, "demo/app", "delete"); err == nil {
		t.Fatal("anonymous delete must remain denied unless configured")
	}
	if err := authenticator.ApplyPolicy(RegistryPolicy{
		Realm:            "https://new.example",
		Service:          "new-service",
		ChallengeMode:    "bearer",
		AllowAnonymous:   true,
		AnonymousActions: []string{"pull", "push", "delete", "catalog"},
		TokenTTL:         2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authorize(request.Context(), request, "demo/app", "delete"); err != nil {
		t.Fatalf("anonymous delete should be allowed when configured: %v", err)
	}
	if _, err := authenticator.Authorize(request.Context(), request, "", "catalog"); err != nil {
		t.Fatalf("anonymous catalog should be allowed when configured: %v", err)
	}
}
