package cmd

import (
	"encoding/json"
	"log"
	"strings"

	"swol/config"
)

// machineConfigDetails serializes the per-machine configuration for the
// collapsible UI. Optional overrides are included when set; flags always
// reflect their effective values. Secrets are never shipped to the browser.
func machineConfigDetails(m config.Machine, schedules []config.Schedule) string {
	return machineConfigDetailsWithPrivacy(m, schedules, false)
}

func machineConfigDetailsWithPrivacy(m config.Machine, schedules []config.Schedule, maskAddresses bool) string {
	details := map[string]any{
		"name":         m.Name,
		"mac":          m.Mac,
		"show_mac":     m.MACVisible(),
		"show_ip":      m.IPVisible(),
		"show_details": m.DetailsVisible(),
		"sol_enabled":  m.SOLEnabled,
	}
	if m.Icon != "" {
		details["icon"] = m.Icon
	}
	if m.IP != nil {
		details["ip"] = *m.IP
	}
	if m.Broadcast != nil {
		details["broadcast"] = map[string]any{"address": m.Broadcast.Address, "port": m.Broadcast.Port}
	}
	if maskAddresses {
		details["mac"] = "[masked]"
		if m.IP != nil {
			details["ip"] = "[masked]"
		}
		if m.Broadcast != nil {
			details["broadcast"] = map[string]any{"address": "[masked]", "port": m.Broadcast.Port}
		}
	}

	if m.SOLIP != "" {
		if maskAddresses {
			details["sol_ip"] = "[masked]"
		} else {
			details["sol_ip"] = m.SOLIP
		}
	}
	if m.SOLPort != 0 {
		details["sol_port"] = m.SOLPort
	}
	if m.SOLProtocol != "" {
		details["sol_protocol"] = m.SOLProtocol
	}
	if m.SOLPath != "" {
		details["sol_path"] = m.SOLPath
	}
	if m.SOLFormat != "" {
		details["sol_format"] = m.SOLFormat
	}
	if m.SOLAuthUsername != "" {
		details["sol_auth_username"] = m.SOLAuthUsername
	}
	if m.SOLAuthPassword != "" {
		details["sol_auth_password"] = "[redacted]"
	}
	if m.SOLConnectTimeout != "" {
		details["sol_connect_timeout"] = m.SOLConnectTimeout
	}
	if m.SOLTimeout != "" {
		details["sol_timeout"] = m.SOLTimeout
	}
	if len(m.SOLHeaders) > 0 {
		details["sol_headers"] = redactSecrets(m.SOLHeaders)
	}
	if len(m.SOLQueryParams) > 0 {
		details["sol_query_params"] = redactSecrets(m.SOLQueryParams)
	}
	if len(schedules) > 0 {
		scheduleData := make([]map[string]string, 0, len(schedules))
		for _, schedule := range schedules {
			entry := map[string]string{"machine": schedule.Machine, "cron": schedule.Cron}
			if schedule.Name != "" {
				entry["name"] = schedule.Name
			}
			scheduleData = append(scheduleData, entry)
		}
		details["schedules"] = scheduleData
	}

	encoded, err := json.MarshalIndent(details, "", "  ")
	if err != nil {
		// All configured values are regular JSON-compatible Go types.
		log.Printf("Failed to prepare configuration details for %q: %v", m.Name, err)
		return "Configuration details unavailable"
	}
	return string(encoded)
}

func redactSecrets(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
		sensitive := false
		for _, term := range []string{"authorization", "cookie", "secret", "password", "passwd", "token", "key", "credential", "auth", "session", "private", "access", "signature", "jwt", "bearer"} {
			if strings.Contains(normalized, term) {
				sensitive = true
				break
			}
		}
		if sensitive {
			result[key] = "[redacted]"
		} else {
			result[key] = value
		}
	}
	return result
}
