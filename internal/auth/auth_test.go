package auth

import (
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
