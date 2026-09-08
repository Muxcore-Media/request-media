package reqquality

import "strings"

// Normalize maps household labels (HD / 4K) and common aliases onto the ids
// media-automation already scores against. Unknown values pass through so
// operators can send a concrete profile id.
func Normalize(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "any", "default":
		return ""
	case "4k", "uhd", "2160", "2160p", "ultra-hd", "ultrahd", "ultra_hd":
		return "4k"
	case "hd", "fhd", "1080", "1080p", "720", "720p":
		return "hd"
	default:
		return strings.TrimSpace(raw)
	}
}
