# Agent notes

Context for AI coding agents working in this repo. See `README.md` for user-facing docs.

## Load-bearing details, don't "fix" these without re-checking

- **Recovery command is `AT+CFUN=1`, not `AT+CFUN=1,1`.** The reset variant (`,1`) was tested
  against the real device and did *not* reliably clear the stuck-low-power-mode bug; the plain
  radio-on command did. This looks like a bug (shouldn't a full reset be more thorough?) but it
  isn't — don't "clean this up" back to `1,1` without re-verifying against real hardware.
- **The recovery trigger is deliberately narrow**: only `CFUN == 0` (radio administratively off).
  It must never fire on "radio on, no signal" (a genuine carrier outage) — see `runOnce` in
  `main.go` and the comment there. Broadening this condition would make the agent fight real
  outages instead of just reporting on them.
- **`internal/remotewrite` hand-encodes the Prometheus remote-write protobuf** via
  `google.golang.org/protobuf/encoding/protowire` instead of depending on
  `github.com/prometheus/prometheus/prompb`. This is intentional: that module pulls a large
  dependency tree and forces a newer Go toolchain for one small subpackage. Don't add it back
  without a real reason — the wire format has been stable for years and the hand-rolled encoder is
  tested (see git history for the byte-level decode verification used during development).
- **No `sync.WaitGroup` / goroutine fan-out.** The poll loop is intentionally sequential — one AT
  query, one web fetch, one push, per cycle. Don't add concurrency for its own sake.

## Deployment

- Docker Compose only — **no systemd unit**, by explicit choice. Don't add one.
- Deployed instances live under `/opt/dc/<app>/` on the target host (a personal home-lab
  convention shared with sibling projects), as a `git clone` kept in sync with `git pull`, not a
  copied build artifact.
- `config.yaml` is gitignored and holds the modem's real admin password on deployed hosts. Never
  commit it, never echo its contents into a commit message, PR description, or diff output.
- `agent.instance` in `config.yaml` is the per-deployment identifier and shows up as the Grafana
  dashboard's instance dropdown value — keep it stable once a dashboard/alerts reference it.

## Go version

Both `go.mod` and the Dockerfile's builder image tag (`golang:X.Y.Z-bookworm`) should be bumped
together, never independently — check the target Docker Hub tag actually exists before bumping.
