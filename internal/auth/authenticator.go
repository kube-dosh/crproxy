package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"crproxy/internal/oci"
	"crproxy/internal/store"
)

type Authenticator struct {
	Store            *store.Store
	Signer           *Signer
	Realm            string
	Service          string
	ChallengeMode    string
	AllowAnonymous   bool
	AnonymousActions []string
	DummyHash        string
	policy           atomic.Pointer[RegistryPolicy]
}

type RegistryPolicy struct {
	Realm            string
	Service          string
	ChallengeMode    string
	AllowAnonymous   bool
	AnonymousActions []string
	TokenTTL         time.Duration
}

type ScopeRequest struct {
	Repository string
	Actions    []string
}

// ApplyPolicy atomically switches registry authentication and token signing
// policy for subsequent requests. Existing registry tokens are invalidated when
// Service changes because their audience no longer matches.
func (a *Authenticator) ApplyPolicy(policy RegistryPolicy) error {
	if a == nil || a.Signer == nil {
		return errors.New("authenticator signer is required")
	}
	if policy.Service == "" {
		return errors.New("registry auth service is required")
	}
	if policy.ChallengeMode != "bearer" && policy.ChallengeMode != "basic" {
		return errors.New("registry auth challenge must be bearer or basic")
	}
	actions, err := normalizeAnonymousActions(policy.AllowAnonymous, policy.AnonymousActions)
	if err != nil {
		return err
	}
	policy.AnonymousActions = actions
	if err := a.Signer.Update(policy.Service, policy.TokenTTL); err != nil {
		return err
	}
	a.policy.Store(&policy)
	return nil
}

func (a *Authenticator) currentPolicy() RegistryPolicy {
	if dynamic := a.policy.Load(); dynamic != nil {
		return *dynamic
	}
	ttl := time.Duration(0)
	if a.Signer != nil {
		ttl = a.Signer.TTL()
	}
	return RegistryPolicy{
		Realm:            a.Realm,
		Service:          a.Service,
		ChallengeMode:    a.ChallengeMode,
		AllowAnonymous:   a.AllowAnonymous,
		AnonymousActions: append([]string(nil), a.AnonymousActions...),
		TokenTTL:         ttl,
	}
}

func (a *Authenticator) ServiceName() string {
	return a.currentPolicy().Service
}

func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (*Identity, bool, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, false, nil
	}
	if username, password, ok := r.BasicAuth(); ok {
		identity, err := a.authenticateBasic(ctx, username, password)
		return identity, true, err
	}
	if raw, ok := bearerTokenFromHeader(header); ok {
		if tokenID, secret, ok := ParsePAT(raw); ok {
			identity, err := a.authenticatePAT(ctx, tokenID, secret)
			return identity, true, err
		}
		claims, err := a.Signer.Verify(raw)
		if err != nil {
			return nil, true, ErrUnauthenticated
		}
		return &Identity{
			UserID:     claims.Subject,
			Role:       claims.Role,
			AuthSource: "jwt",
			Claims:     &claims,
		}, true, nil
	}
	return nil, true, ErrUnauthenticated
}

func (a *Authenticator) AuthenticateBasicOrPAT(ctx context.Context, r *http.Request) (*Identity, error) {
	username, password, ok := r.BasicAuth()
	if ok {
		return a.authenticateBasic(ctx, username, password)
	}
	header := r.Header.Get("Authorization")
	if raw, ok := bearerTokenFromHeader(header); ok {
		if tokenID, secret, ok := ParsePAT(raw); ok {
			return a.authenticatePAT(ctx, tokenID, secret)
		}
	}
	return nil, ErrUnauthenticated
}

func bearerTokenFromHeader(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

func (a *Authenticator) authenticateBasic(ctx context.Context, username, password string) (*Identity, error) {
	if tokenID, secret, ok := ParsePAT(password); ok {
		identity, err := a.authenticatePAT(ctx, tokenID, secret)
		if err != nil {
			return nil, err
		}
		if identity.Username != username {
			return nil, ErrUnauthenticated
		}
		identity.AuthSource = "basic_pat"
		return identity, nil
	}
	user, err := a.Store.UserByUsername(ctx, username)
	if err != nil {
		_ = VerifyPassword(a.DummyHash, password)
		return nil, ErrUnauthenticated
	}
	if !user.Enabled || !VerifyPassword(user.PasswordHash, password) {
		return nil, ErrUnauthenticated
	}
	return &Identity{
		UserID:     user.ID,
		Username:   user.Username,
		Role:       user.Role,
		Scopes:     RoleScopes(user.Role),
		AuthSource: "basic",
	}, nil
}

func (a *Authenticator) authenticatePAT(ctx context.Context, tokenID, secret string) (*Identity, error) {
	token, err := a.Store.APITokenByID(ctx, tokenID)
	if err != nil || !token.UserActive || !VerifyPATSecret(token.SecretHash, secret) {
		return nil, ErrUnauthenticated
	}
	now := time.Now().UTC()
	if token.RevokedAt != nil {
		return nil, ErrUnauthenticated
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
		return nil, ErrUnauthenticated
	}
	scopes := IntersectScopes(RoleScopes(token.UserRole), token.Scopes)
	a.Store.TouchAPIToken(ctx, token.ID)
	return &Identity{
		UserID:     token.UserID,
		Username:   token.Username,
		Role:       token.UserRole,
		Scopes:     scopes,
		AuthSource: "pat",
	}, nil
}

func (a *Authenticator) Authorize(ctx context.Context, r *http.Request, repository, action string) (*Identity, error) {
	identity, provided, err := a.Authenticate(ctx, r)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	if identity == nil {
		anonymous := a.anonymousIdentity()
		if anonymous == nil || !anonymous.HasScope(action) {
			return nil, ErrUnauthenticated
		}
		identity = anonymous
	}
	_ = provided
	if action == "catalog" {
		if identity.Anonymous {
			return identity, nil
		}
		if !identity.CanCatalog() {
			return nil, ErrForbidden
		}
		return identity, nil
	}
	if !identity.CanRepository(repository, action) {
		return nil, ErrForbidden
	}
	return identity, nil
}

func (a *Authenticator) AuthenticateBase(ctx context.Context, r *http.Request) (*Identity, error) {
	identity, provided, err := a.Authenticate(ctx, r)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	if identity == nil {
		if anonymous := a.anonymousIdentity(); anonymous != nil {
			return anonymous, nil
		}
		_ = provided
		return nil, ErrUnauthenticated
	}
	return identity, nil
}

func (a *Authenticator) anonymousIdentity() *Identity {
	policy := a.currentPolicy()
	if !policy.AllowAnonymous {
		return nil
	}
	actions, err := normalizeAnonymousActions(true, policy.AnonymousActions)
	if err != nil || len(actions) == 0 {
		return nil
	}
	return &Identity{Role: "anonymous", Scopes: actions, AuthSource: "anonymous", Anonymous: true}
}

func normalizeAnonymousActions(enabled bool, actions []string) ([]string, error) {
	if !enabled {
		return nil, nil
	}
	allowed := map[string]bool{"pull": true, "push": true, "delete": true, "catalog": true}
	seen := make(map[string]bool, len(actions))
	for _, raw := range actions {
		action := strings.ToLower(strings.TrimSpace(raw))
		if !allowed[action] {
			return nil, fmt.Errorf("unsupported anonymous registry action %q", raw)
		}
		seen[action] = true
	}
	result := make([]string, 0, len(seen))
	for _, action := range []string{"pull", "push", "delete", "catalog"} {
		if seen[action] {
			result = append(result, action)
		}
	}
	if len(result) == 0 {
		result = []string{"pull"}
	}
	return result, nil
}

func (a *Authenticator) Challenge(repository string, actions []string) string {
	policy := a.currentPolicy()
	if policy.ChallengeMode == "basic" {
		return a.BasicChallenge()
	}
	value := fmt.Sprintf(`Bearer realm="%s/v2/token",service="%s"`, policy.Realm, policy.Service)
	if repository != "" && len(actions) > 0 {
		value += fmt.Sprintf(`,scope="repository:%s:%s"`, repository, joinActions(actions))
	}
	return value
}

func (a *Authenticator) BasicChallenge() string {
	return `Basic realm="crproxy"`
}

func (a *Authenticator) IssueToken(identity *Identity, scopes []ScopeRequest) (string, Claims, error) {
	if identity == nil || identity.Claims != nil {
		return "", Claims{}, ErrUnauthenticated
	}
	var access []Access
	for _, scope := range scopes {
		if err := oci.ValidateRepository(scope.Repository); err != nil {
			return "", Claims{}, fmt.Errorf("invalid repository in scope")
		}
		actions := NormalizeActions(scope.Actions)
		for _, action := range actions {
			if action == "*" {
				return "", Claims{}, ErrForbidden
			}
			if !identity.CanRepository(scope.Repository, action) {
				return "", Claims{}, ErrForbidden
			}
		}
		if len(actions) > 0 {
			access = append(access, Access{Type: "repository", Name: scope.Repository, Actions: actions})
		}
	}
	return a.Signer.Issue(identity.UserID, identity.Role, identity.AuthSource, access)
}

func joinActions(actions []string) string {
	if len(actions) == 0 {
		return "pull"
	}
	result := actions[0]
	for _, action := range actions[1:] {
		result += "," + action
	}
	return result
}

func IsAuthError(err error) bool {
	return errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrForbidden)
}
