package internal

import "testing"

func TestRolesCanRequest(t *testing.T) {
	m := NewModule(Config{})
	if !m.rolesCanRequest([]string{"user"}) {
		t.Fatal("user should request")
	}
	if m.rolesCanRequest([]string{"viewer"}) {
		t.Fatal("viewer should not request by default")
	}
	if m.rolesCanRequest(nil) {
		t.Fatal("empty roles should deny unless REQUEST_ALLOW_ANONYMOUS=1")
	}
	t.Setenv("REQUEST_ALLOW_ANONYMOUS", "1")
	if !m.rolesCanRequest(nil) {
		t.Fatal("empty roles allowed when REQUEST_ALLOW_ANONYMOUS=1")
	}
}

func TestRolesCanApprove(t *testing.T) {
	if !rolesCanApprove([]string{"admin"}) {
		t.Fatal("admin should approve")
	}
	if !rolesCanApprove([]string{"manager"}) {
		t.Fatal("manager should approve")
	}
	if rolesCanApprove([]string{"user"}) {
		t.Fatal("user should not approve")
	}
}

func TestManagerAutoApprove(t *testing.T) {
	m := NewModule(Config{})
	if m.needsApproval("mgr", []string{"manager"}) {
		t.Fatal("manager should skip approval")
	}
	if !m.needsApproval("alice", []string{"user"}) {
		t.Fatal("user should need approval")
	}
}

func TestAllowedRolesSetting(t *testing.T) {
	m := NewModule(Config{})
	m.cfgMu.Lock()
	m.allowedRequestRoles = "viewer"
	m.cfgMu.Unlock()
	if !m.rolesCanRequest([]string{"viewer"}) {
		t.Fatal("viewer allowed when configured")
	}
}
