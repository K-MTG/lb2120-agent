# LB2120 Agent

A Go application that monitors a Netgear LB2120 LTE modem, pushes Prometheus metrics to a
remote_write endpoint (e.g. VictoriaMetrics), and applies a rate-limited automatic recovery when
it detects the modem's known firmware bug: after certain resets/reboots, the cellular radio gets
stuck administratively powered off (`AT+CFUN=0`, reported via the web UI as `PMState: LowPower` /
`SmState: LowPowerMode`) and never recovers on its own. `AT+CFUN=1` reliably clears it; the
device's own full-reset command (`AT+CFUN=1,1`) does not.

## Why this exists

This LB2120 serves as an LTE WAN failover link behind a UniFi gateway. The stuck-radio bug was
reproduced twice: once after a manual `AT+CFUN=1,1` reset, and again after a plain device reboot
(triggered by toggling the web UI's Diagnostics setting) — both times with continuous, hardwired
power the whole time, which rules out an actual power-loss event. The device is EOL/unsupported,
so there's no firmware fix coming; this agent exists to make the workaround automatic instead of
manual.

## Features

- Polls the LB2120's AT-command interface (port 5510) every 5 minutes for radio/registration
  state (`CFUN`, `CSQ`, `CREG`/`CEREG`, `CGATT`, `COPS`) — this is the authoritative, low-fuss
  source, with no session/cookie management needed.
- Also polls the web JSON API (`/api/model.json`) for supplementary signal detail (RSRP/RSRQ/SINR),
  SIM status, and power state. This is best-effort: if login/session handling hiccups, that
  cycle's metrics just degrade gracefully instead of the agent crashing.
- Pushes everything to a Prometheus remote_write endpoint using the actual wire protocol
  (protobuf + snappy), with no dependency on the full `prometheus/prometheus` module or a
  separate scrape agent — one small static binary.
- **Recovery logic that won't fight a real outage.** It only ever sends `AT+CFUN=1` when the
  specific "radio administratively off" fingerprint (`CFUN==0`) is observed — never for "radio on,
  no coverage", which is what an actual carrier outage looks like. Even then:
  - Requires the stuck state to persist across `recovery.confirm_polls` consecutive polls before
    acting (default: 2, i.e. ~10 minutes), so a normal boot sequence isn't mistaken for the bug.
  - Won't attempt again within `recovery.cooldown` of the last attempt (default: 15 minutes).
  - After `recovery.max_failures` consecutive attempts that didn't clear the stuck state (default:
    3), it stops trying and enters backoff for `recovery.backoff` (default: 2 hours) — surfaced via
    a metric rather than hammering the modem indefinitely.
  - This state is persisted to disk (`agent.state_file`) so a container restart mid-backoff
    doesn't forget the failure streak.

## Metrics

All metrics carry `job` and `instance` labels (from `agent.job` / `agent.instance`). Notable ones:

| Metric | Meaning |
|---|---|
| `lb2120_up` | Whether the last AT-interface poll succeeded |
| `lb2120_web_up` | Whether the last web API login+fetch succeeded |
| `lb2120_radio_functional` | `CFUN == 1` |
| `lb2120_registered` | Registered on the LTE network |
| `lb2120_attached` | Attached to packet data service |
| `lb2120_wwan_connected` | Web API's connection state (`Connected`) |
| `lb2120_signal_rssi_dbm` / `_rsrp_dbm` / `_rsrq_db` / `_sinr_db` / `_bars` | Signal quality |
| `lb2120_stuck` | The known-bug fingerprint is currently true — **distinct** from `lb2120_wwan_connected`, so an alert on "not connected" (could be a real outage) can be told apart from "known bug, agent is/should be handling it" |
| `lb2120_stuck_duration_seconds` | How long the current stuck episode has lasted |
| `lb2120_recovery_attempts_total` / `_successes_total` / `_failures_total` | Recovery action counters |
| `lb2120_recovery_backoff_active` | Agent has given up retrying for now and needs a human |
| `lb2120_reset_reason` | The modem's own `power.resetreason` value |
| `lb2120_info` | Static info (model, firmware/hardware version) as labels |

Grafana alerting isn't set up by this project — wire up alerts on top of these once they're
flowing into your TSDB (e.g. alert on `lb2120_wwan_connected == 0` for real outages, and
separately on `lb2120_recovery_backoff_active == 1` for "the agent has given up, go look").

## Configuration

Copy `config.example.yaml` to `config.yaml` and fill it in (`config.yaml` is gitignored since it
holds the modem's admin password):

```sh
cp config.example.yaml config.yaml
```

```yaml
lb2120:
  device_ip: "192.168.1.1"
  at_port: 5510
  password: "your_lb2120_admin_password"

remote_write:
  url: "http://your-victoriametrics-host:8480/insert/<tenant>/prometheus/api/v1/write"

agent:
  poll_interval: "5m"
  instance: "lb2120"
  job: "lb2120_agent"
  state_file: "/var/lib/lb2120-agent/state.json"

recovery:
  confirm_polls: 2
  cooldown: "15m"
  max_failures: 3
  backoff: "2h"
```

**Fields:**
- `lb2120.device_ip` / `at_port`: where to reach the modem's AT-command telnet interface (this
  requires "Enable Diagnostics" to be turned on in the LB2120's web UI under Settings → Advanced →
  LAN — that interface is unauthenticated, so make sure network access to it is restricted to
  whatever host is running this agent).
- `lb2120.password`: the LB2120 web UI admin password.
- `remote_write.url`: full remote_write endpoint, including the `/api/v1/write` suffix (for a
  VictoriaMetrics cluster this is `/insert/<accountID>/prometheus/api/v1/write`).
- `agent.state_file`: where recovery state is persisted; should point at the bind-mounted volume
  when running in Docker (see below).
- `recovery.*`: see [Recovery logic](#features) above.

## Running with Docker Compose

Build and run the app using Docker Compose:

```sh
docker compose up -d
```

- No inbound ports are exposed — this agent only makes outbound connections to the LB2120 and to
  the remote_write endpoint.
- Recovery state is bind-mounted at `./volumes/state` so it survives container restarts.

To deploy on a remote host, clone the repo there directly and set up `config.yaml` the same way:

```sh
git clone https://github.com/<you>/lb2120-agent.git
cd lb2120-agent
cp config.example.yaml config.yaml   # then edit in your real values
docker compose up -d
```

## Grafana Dashboard

Import [`grafana/lb2120-agent-dashboard.json`](grafana/lb2120-agent-dashboard.json) into Grafana
(Dashboards → New → Import → upload JSON file). It'll prompt you to pick a Prometheus-compatible
datasource, then exposes an **instance** dropdown (populated from the `instance` label, e.g.
`ch-lb2120`) so it works across multiple deployed LB2120s if you run more than one. Panels cover
live status, the known-bug recovery state/history, signal quality, and a device/SIM/power info
table. There's no built-in alerting — set up Grafana alert rules on top of `lb2120_wwan_connected`
and `lb2120_recovery_backoff_active` per the note above once this is wired in.
