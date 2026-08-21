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
			Description: "When true, all new requests start as pending until approved (REQUEST_REQUIRE_APPROVAL). Non-admin requestors always need approval.",
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
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) getPreferWorkflow() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.preferWorkflow
}
