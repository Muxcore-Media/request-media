package internal

import (
	"os"
	"strings"
)

const defaultAllowedRequestRoles = "admin,manager,user"

func rolesFromIsAdmin(isAdmin bool) []string {
	if isAdmin {
		return []string{"admin"}
	}
	return []string{"user"}
}

func (m *Module) getAllowedRequestRoles() []string {
	m.cfgMu.RLock()
	raw := m.allowedRequestRoles
	m.cfgMu.RUnlock()
	if raw == "" {
		raw = defaultAllowedRequestRoles
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"admin", "manager", "user"}
	}
	return out
}

func (m *Module) getManagerAutoApprove() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.managerAutoApprove
}

func allowAnonymousRequest() bool {
	return envFlagTruthy(os.Getenv("REQUEST_ALLOW_ANONYMOUS"))
}

func envFlagTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (m *Module) rolesCanRequest(roles []string) bool {
	if len(roles) == 0 {
		return allowAnonymousRequest()
	}
	allowed := m.getAllowedRequestRoles()
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, r := range allowed {
		allowedSet[strings.ToLower(r)] = struct{}{}
	}
	for _, r := range roles {
		if _, ok := allowedSet[strings.ToLower(r)]; ok {
			return true
		}
	}
	return false
}

func isPrivilegedRequestor(roles []string, managerAutoApprove bool) bool {
	for _, r := range roles {
		switch strings.ToLower(strings.TrimSpace(r)) {
		case "admin":
			return true
		case "manager":
			if managerAutoApprove {
				return true
			}
		}
	}
	return false
}

func rolesCanApprove(roles []string) bool {
	for _, r := range roles {
		switch strings.ToLower(strings.TrimSpace(r)) {
		case "admin", "manager":
			return true
		}
	}
	return false
}

func containsRole(roles []string, want string) bool {
	want = strings.ToLower(want)
	for _, r := range roles {
		if strings.ToLower(strings.TrimSpace(r)) == want {
			return true
		}
	}
	return false
}

func parseAllowedRolesEnv() string {
	if v := strings.TrimSpace(os.Getenv("REQUEST_ALLOWED_ROLES")); v != "" {
		return v
	}
	return defaultAllowedRequestRoles
}
