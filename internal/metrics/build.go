package metrics

import (
	"time"

	"github.com/K-MTG/lb2120-agent/internal/lb2120"
	"github.com/K-MTG/lb2120-agent/internal/remotewrite"
)

// Snapshot is everything one poll cycle learned, used to build the sample
// batch pushed to remote_write.
type Snapshot struct {
	AT       *lb2120.ATStatus // nil if the AT query failed
	Model    *lb2120.Model    // nil if the web fetch/login failed
	Stuck    bool
	Decision Decision
	State    *State

	ScrapeDurationSeconds float64
	ATError               error
	WebError              error
}

func gauge(name string, value float64, labels map[string]string) remotewrite.Sample {
	return remotewrite.Sample{Name: name, Value: value, Labels: labels}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Build turns one poll's snapshot into the flat sample batch to push.
func Build(snap Snapshot) []remotewrite.Sample {
	var out []remotewrite.Sample
	noLabels := map[string]string{}

	out = append(out, gauge("lb2120_up", b2f(snap.ATError == nil), noLabels))
	out = append(out, gauge("lb2120_scrape_duration_seconds", snap.ScrapeDurationSeconds, noLabels))
	out = append(out, gauge("lb2120_web_up", b2f(snap.WebError == nil), noLabels))

	if at := snap.AT; at != nil {
		out = append(out, gauge("lb2120_radio_functional", b2f(at.CFUN == 1), noLabels))
		out = append(out, gauge("lb2120_registered", b2f(at.CEREGStat == 1 || at.CEREGStat == 5), noLabels))
		out = append(out, gauge("lb2120_attached", b2f(at.CGATT == 1), noLabels))
		out = append(out, gauge("lb2120_csq_rssi_index", float64(at.CSQRSSI), noLabels))
		if at.COPSOperator != "" {
			out = append(out, gauge("lb2120_operator_info", 1, map[string]string{
				"operator": at.COPSOperator,
			}))
		}
	}

	if m := snap.Model; m != nil {
		out = append(out, gauge("lb2120_wwan_connected", b2f(m.WWAN.Connection == "Connected"), noLabels))
		out = append(out, gauge("lb2120_signal_rssi_dbm", m.WWAN.SignalStrength.RSSI, noLabels))
		out = append(out, gauge("lb2120_signal_rsrp_dbm", m.WWAN.SignalStrength.RSRP, noLabels))
		out = append(out, gauge("lb2120_signal_rsrq_db", m.WWAN.SignalStrength.RSRQ, noLabels))
		out = append(out, gauge("lb2120_signal_sinr_db", m.WWAN.SignalStrength.SINR, noLabels))
		out = append(out, gauge("lb2120_signal_bars", m.WWAN.SignalStrength.Bars, noLabels))
		out = append(out, gauge("lb2120_roaming", b2f(m.WWAN.Roaming), noLabels))
		out = append(out, gauge("lb2120_alert_wwan_disconnected", b2f(m.HasActiveWWANDisconnectedAlert()), noLabels))
		out = append(out, gauge("lb2120_power_state_info", 1, map[string]string{
			"pmstate": m.Power.PMState,
			"smstate": m.Power.SmState,
		}))
		out = append(out, gauge("lb2120_reset_reason", float64(m.Power.ResetReason), noLabels))
		out = append(out, gauge("lb2120_device_temperature_celsius", m.General.DevTemperature, noLabels))
		out = append(out, gauge("lb2120_device_temp_critical", b2f(m.Power.DeviceTempCritical), noLabels))
		out = append(out, gauge("lb2120_cell_id", m.WWANAdv.CellID, noLabels))
		out = append(out, gauge("lb2120_radio_quality", m.WWANAdv.RadioQuality, noLabels))
		// Cumulative within the billing cycle (resets when it rolls over);
		// use increase()/delta() over a window in Grafana for usage-per-
		// period views. increase() handles the cycle-reset drop correctly.
		out = append(out, gauge("lb2120_data_transferred_bytes", m.WWAN.DataUsage.Generic.DataTransferred, noLabels))
		out = append(out, gauge("lb2120_sim_status_info", 1, map[string]string{
			"status": m.SIM.Status,
		}))
		out = append(out, gauge("lb2120_diagnostics_enabled", b2f(m.Custom.AtTcpEnable), noLabels))
		if m.WWANAdv.CurBand != "" {
			out = append(out, gauge("lb2120_band_info", 1, map[string]string{
				"band": m.WWANAdv.CurBand,
			}))
		}
		out = append(out, gauge("lb2120_info", 1, map[string]string{
			"model":        m.General.Model,
			"fw_version":   m.General.FWversion,
			"hw_version":   m.General.HWversion,
			"manufacturer": m.General.Manufacturer,
		}))
	}

	// The known-bug fingerprint, distinct from lb2120_wwan_connected: this
	// is specifically "radio administratively off", not "no coverage".
	out = append(out, gauge("lb2120_stuck", b2f(snap.Stuck), noLabels))

	if s := snap.State; s != nil {
		out = append(out, gauge("lb2120_recovery_attempts_total", float64(s.AttemptsTotal), noLabels))
		out = append(out, gauge("lb2120_recovery_successes_total", float64(s.SuccessesTotal), noLabels))
		out = append(out, gauge("lb2120_recovery_failures_total", float64(s.FailuresTotal), noLabels))
		out = append(out, gauge("lb2120_recovery_backoff_active", b2f(snap.Decision.InBackoff), noLabels))
		if !s.StuckSince.IsZero() {
			out = append(out, gauge("lb2120_stuck_duration_seconds", time.Since(s.StuckSince).Seconds(), noLabels))
		} else {
			out = append(out, gauge("lb2120_stuck_duration_seconds", 0, noLabels))
		}
	}

	return out
}
