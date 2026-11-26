package auth

import (
	"errors"
	"strings"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("access denied")
)

var roleScopes = map[string][]string{
	"admin":    {"pull", "push", "delete", "manage", "admin"},
	"operator": {"pull", "push", "delete", "manage"},
	"viewer":   {"pull"},
}

type Identity struct {
	UserID     string
	Username   string
	Role       string
	Scopes     []string
	AuthSource string
	Claims     *Claims
	Anonymous  bool
}

func RoleScopes(role string) []string {
	return append([]string(nil), roleScopes[role]...)
}

func IntersectScopes(a, b []string) []string {
	set := make(map[string]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	var result []string
	for _, value := range b {
		if set[value] {
			result = append(result, value)
		}
	}
	return result
}

func (i *Identity) HasScope(scope string) bool {
	if i == nil {
		return false
	}
	for _, value := range i.Scopes {
		if value == scope {
			return true
		}
	}
	return false
}

func (i *Identity) HasAnyScope(scopes ...string) bool {
	for _, scope := range scopes {
		if i.HasScope(scope) {
			return true
		}
	}
	return false
}

func (i *Identity) CanRepository(repository, action string) bool {
	if i == nil {
		return false
	}
	if i.Claims != nil {
		for _, access := range i.Claims.Access {
			if access.Type != "repository" || access.Name != repository {
				continue
			}
			for _, granted := range access.Actions {
				if granted == action || granted == "*" {
					return true
				}
			}
		}
		return false
	}
	return i.HasScope(action)
}

func (i *Identity) CanCatalog() bool {
	if i == nil || i.Claims != nil {
		return false
	}
	return i.HasAnyScope("manage", "admin")
}

func NormalizeActions(actions []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, action := range actions {
		action = strings.TrimSpace(action)
		switch action {
		case "pull", "push", "delete", "*":
			if !seen[action] {
				seen[action] = true
				result = append(result, action)
			}
		}
	}
	return result
}
