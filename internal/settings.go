package internal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
)

func (m *Module) Settings() []contracts.SettingDef {
	return m.settingsDefs()
}

func (m *Module) UpdateSetting(key, value string) error {
	return m.updateSetting(key, value)
}

func (m *Module) settingsDefs() []contracts.SettingDef {
	return []contracts.SettingDef{
		{
			Key:         "prefer_workflow",
			Label:       "Prefer Workflow Engine",
			Type:        contracts.SettingTypeBool,
			Value:       strconv.FormatBool(m.getPreferWorkflow()),
			Default:     "true",
			Description: "Try workflow.engine (movie-request/tv-request) before library Add* (REQUEST_PREFER_WORKFLOW)",
			Group:       "Routing",
		},
		{
			Key:         "require_approval",
			Label:       "Require Approval",
			Type:        contracts.SettingTypeBool,
			Value:       strconv.FormatBool(m.getRequireApproval()),
			Default:     "false",
			Description: "When true, all new requests start as pending until approved (REQUEST_REQUIRE_APPROVAL). Non-privileged requestors need approval when false.",
			Group:       "Approval",
		},
		{
			Key:         "allowed_request_roles",
			Label:       "Allowed Request Roles",
			Type:        contracts.SettingTypeString,
			Value:       m.getAllowedRequestRolesCSV(),
			Default:     defaultAllowedRequestRoles,
			Description: "Comma-separated roles that may submit requests (REQUEST_ALLOWED_ROLES). Default: admin,manager,user — viewers excluded.",
			Group:       "Approval",
		},
		{
			Key:         "manager_auto_approve",
			Label:       "Manager Auto-Approve",
			Type:        contracts.SettingTypeBool,
			Value:       strconv.FormatBool(m.getManagerAutoApprove()),
			Default:     "true",
			Description: "When true, manager role skips approval queue like admin (REQUEST_MANAGER_AUTO_APPROVE).",
			Group:       "Approval",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "prefer_workflow", "REQUEST_PREFER_WORKFLOW":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid prefer_workflow %q (true/false)", value)
		}
		m.cfgMu.Lock()
		m.preferWorkflow = v
		m.cfgMu.Unlock()
		return nil
	case "require_approval", "REQUEST_REQUIRE_APPROVAL":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid require_approval %q (true/false)", value)
		}
		m.cfgMu.Lock()
		m.requireApproval = v
		m.cfgMu.Unlock()
		return nil
	case "allowed_request_roles", "REQUEST_ALLOWED_ROLES":
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("allowed_request_roles cannot be empty")
		}
		m.cfgMu.Lock()
		m.allowedRequestRoles = value
		m.cfgMu.Unlock()
		return nil
	case "manager_auto_approve", "REQUEST_MANAGER_AUTO_APPROVE":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid manager_auto_approve %q (true/false)", value)
		}
		m.cfgMu.Lock()
		m.managerAutoApprove = v
		m.cfgMu.Unlock()
		return nil
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) getPreferWorkflow() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.preferWorkflow
}

func (m *Module) getAllowedRequestRolesCSV() string {
	roles := m.getAllowedRequestRoles()
	return strings.Join(roles, ",")
}
