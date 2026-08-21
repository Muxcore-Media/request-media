package internal

import "testing"

func TestNormalizeTMDBArtPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/abc.jpg", "/abc.jpg"},
		{"https://image.tmdb.org/t/p/w500/abc.jpg", "/abc.jpg"},
		{"https://image.tmdb.org/t/p/original/xyz.png", "/xyz.png"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeTMDBArtPath(tc.in); got != tc.want {
			t.Fatalf("normalizeTMDBArtPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
