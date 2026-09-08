package reqquality

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"any":    "",
		"4K":     "4k",
		"uhd":    "4k",
		"2160p":  "4k",
		"HD":     "hd",
		"1080p":  "hd",
		"qp_uhd": "qp_uhd",
		"  4k  ": "4k",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
