package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

var nextLinkPattern = regexp.MustCompile(`(?i)<([^>]+)>\s*;\s*rel="?next"?`)

func rewriteNextLink(link, basePath string) string {
	match := nextLinkPattern.FindStringSubmatch(link)
	if len(match) != 2 {
		return ""
	}
	value := match[1]
	rawQuery := ""
	if _, query, ok := strings.Cut(value, "?"); ok {
		rawQuery = query
	}
	if rawQuery != "" {
		return fmt.Sprintf(`<%s?%s>; rel="next"`, basePath, rawQuery)
	}
	return fmt.Sprintf(`<%s>; rel="next"`, basePath)
}
