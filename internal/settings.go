package internal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/request-media/internal/reqquota"
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
			Default:     "true",
			Description: "Hold new requests in pending until approved (REQUEST_REQUIRE_APPROVAL)",
			Group:       "Approval",
		},
		{
			Key:         "max_pending_per_user",
			Label:       "Max pending requests per user",
			Type:        contracts.SettingTypeInt,
			Value:       strconv.Itoa(m.getQuotaPolicy().MaxPendingPerUser),
			Default:     "0",
			Description: "0 = unlimited. Household members cannot submit more until an approver acts (REQUEST_MAX_PENDING_PER_USER)",
			Group:       "Approval",
		},
		{
			Key:         "max_requests_per_week",
			Label:       "Max requests per user per week",
			Type:        contracts.SettingTypeInt,
			Value:       strconv.Itoa(m.getQuotaPolicy().MaxPerWeek),
			Default:     "0",
			Description: "0 = unlimited rolling 7-day cap (REQUEST_MAX_PER_WEEK)",
			Group:       "Approval",
		},
		{
			Key:         "auto_approve_users",
			Label:       "Auto-approve users",
			Type:        contracts.SettingTypeString,
			Value:       strings.Join(m.getQuotaPolicy().AutoApproveUsers, ", "),
			Default:     "",
			Description: "Comma-separated user ids that skip the pending queue (REQUEST_AUTO_APPROVE_USERS)",
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
	case "max_pending_per_user", "REQUEST_MAX_PENDING_PER_USER":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid max_pending_per_user %q (integer >= 0)", value)
		}
		p := m.getQuotaPolicy()
		p.MaxPendingPerUser = n
		m.setQuotaPolicy(p)
		return nil
	case "max_requests_per_week", "REQUEST_MAX_PER_WEEK":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid max_requests_per_week %q (integer >= 0)", value)
		}
		p := m.getQuotaPolicy()
		p.MaxPerWeek = n
		m.setQuotaPolicy(p)
		return nil
	case "auto_approve_users", "REQUEST_AUTO_APPROVE_USERS":
		p := m.getQuotaPolicy()
		p.AutoApproveUsers = reqquota.ParseUsers(value)
		m.setQuotaPolicy(p)
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
