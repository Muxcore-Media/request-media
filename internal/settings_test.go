package internal

import "testing"

func TestSettingsPreferWorkflow(t *testing.T) {
	m := NewModule(Config{})
	if !m.getPreferWorkflow() {
		t.Fatal("default prefer_workflow should be true")
	}
	defs := m.Settings()
	if len(defs) != 1 || defs[0].Key != "prefer_workflow" {
		t.Fatalf("defs=%+v", defs)
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
