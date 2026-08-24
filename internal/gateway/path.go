package gateway

import (
	pathpkg "path"
	"regexp"
	"strings"
)

var bearerPattern = regexp.MustCompile(`(?i)^bearer[[:space:]]+([^[:space:]]+)[[:space:]]*$`)

var publicPathPrefixes = [...]string{
	"/v1",
	"/v1beta",
	"/backend-api/codex",
	"/api/provider",
}

// AllowedPublicPath mirrors the current Gateway's normalized-URI allowlist.
// Callers must pass URL.Path, never RequestURI, so query strings cannot affect
// routing decisions.
func AllowedPublicPath(path string) bool {
	_, ok := NormalizePublicPath(path)
	return ok
}

// NormalizePublicPath returns the canonical path that is safe to forward.
// Checking and forwarding the same normalized value prevents dot-segment or
// duplicate-slash differences between Gin and the selected upstream.
func NormalizePublicPath(path string) (string, bool) {
	if path == "" || path[0] != '/' {
		return "", false
	}
	normalized := pathpkg.Clean(path)
	if normalized == "/v1internal:method" {
		return normalized, true
	}
	for _, prefix := range publicPathPrefixes {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"/") {
			return normalized, true
		}
	}
	return "", false
}

// ExtractBearer accepts the same case-insensitive scheme and surrounding
// whitespace as the OpenResty request gate, while rejecting embedded
// whitespace in the Key itself.
func ExtractBearer(header string) (string, bool) {
	match := bearerPattern.FindStringSubmatch(header)
	if len(match) != 2 || match[1] == "" {
		return "", false
	}
	return match[1], true
}
