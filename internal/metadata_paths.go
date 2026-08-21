package internal

import "strings"

// normalizeTMDBArtPath accepts TMDB relative paths (/…), absolute TMDB URLs, or passthrough URLs.
func normalizeTMDBArtPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	const marker = "image.tmdb.org/t/p/"
	if idx := strings.Index(raw, marker); idx >= 0 {
		rest := raw[idx+len(marker):]
		if slash := strings.Index(rest, "/"); slash >= 0 && slash+1 < len(rest) {
			return rest[slash:]
		}
	}
	return raw
}
