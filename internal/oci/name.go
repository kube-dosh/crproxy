package oci

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	repositoryComponentPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	tagPattern                 = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

func ValidateRepository(name string) error {
	if name == "" || len(name) > 255 {
		return fmt.Errorf("invalid repository name")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return fmt.Errorf("invalid repository name")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || !repositoryComponentPattern.MatchString(part) {
			return fmt.Errorf("invalid repository name")
		}
	}
	return nil
}

func ValidateTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("invalid tag")
	}
	return nil
}

func IsDigestReference(reference string) bool {
	return strings.Contains(reference, ":")
}
