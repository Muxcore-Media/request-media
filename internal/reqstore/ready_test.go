package reqstore

import (
	"path/filepath"
	"testing"
)

func TestClaimReadyNotified(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	first, err := s.ClaimReadyNotified("req_1")
	if err != nil || !first {
		t.Fatalf("first claim: claimed=%v err=%v", first, err)
	}
	second, err := s.ClaimReadyNotified("req_1")
	if err != nil || second {
		t.Fatalf("second claim should be false: claimed=%v err=%v", second, err)
	}
}
