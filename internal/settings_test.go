package internal

import "testing"

func TestSettingsPreferWorkflow(t *testing.T) {
	m := NewModule(Config{})
	if !m.getPreferWorkflow() {
		t.Fatal("default prefer_workflow should be true")
	}
	if !m.getRequireApproval() {
		t.Fatal("default require_approval should be true")
	}
	defs := m.Settings()
	keys := map[string]bool{}
	for _, d := range defs {
		keys[d.Key] = true
	}
	for _, want := range []string{"prefer_workflow", "require_approval", "max_pending_per_user", "max_requests_per_week", "auto_approve_users"} {
		if !keys[want] {
			t.Fatalf("missing setting %s in %+v", want, defs)
		}
	}
	if err := m.UpdateSetting("prefer_workflow", "false"); err != nil {
		t.Fatal(err)
	}
	if m.getPreferWorkflow() {
		t.Fatal("expected false")
	}
	if err := m.UpdateSetting("REQUEST_PREFER_WORKFLOW", "true"); err != nil {
		t.Fatal(err)
	}
	if !m.getPreferWorkflow() {
		t.Fatal("expected true")
	}
	if err := m.UpdateSetting("unknown", "x"); err == nil {
		t.Fatal("expected error")
	}
}
