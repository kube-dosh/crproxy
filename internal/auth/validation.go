package auth

import (
	"fmt"
	"regexp"
	"strings"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("username must be 1-64 characters and use only letters, numbers, dot, underscore or hyphen")
	}
	return nil
}

func ValidatePassword(username, password string) error {
	if len(password) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}
	if len(password) > 1024 {
		return fmt.Errorf("password is too long")
	}
	if username != "" && strings.EqualFold(password, username) {
		return fmt.Errorf("password must not equal username")
	}
	return nil
}

func ValidateRole(role string) error {
	switch role {
	case "admin", "operator", "viewer":
		return nil
	default:
		return fmt.Errorf("invalid role")
	}
}
