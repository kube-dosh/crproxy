package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"crproxy/internal/oci"
	"crproxy/internal/store"
)

type Authenticator struct {
	Store          *store.Store
	Signer         *Signer
	Realm          string
	Service        string
	ChallengeMode  string
	AllowAnonymous bool
	DummyHash      string
}

type ScopeRequest struct {
	Repository string
	Actions    []string
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
		if !a.AllowAnonymous {
			return nil, ErrUnauthenticated
		}
		if action != "pull" {
			return nil, ErrUnauthenticated
		}
		identity = &Identity{Role: "anonymous", Scopes: []string{"pull"}, AuthSource: "anonymous", Anonymous: true}
	}
	_ = provided
	if action == "catalog" {
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
		if a.AllowAnonymous {
			return &Identity{Role: "anonymous", Scopes: []string{"pull"}, AuthSource: "anonymous", Anonymous: true}, nil
		}
		_ = provided
		return nil, ErrUnauthenticated
	}
	return identity, nil
}

func (a *Authenticator) Challenge(repository string, actions []string) string {
	if a.ChallengeMode == "basic" {
		return a.BasicChallenge()
	}
	value := fmt.Sprintf(`Bearer realm="%s/v2/token",service="%s"`, a.Realm, a.Service)
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
