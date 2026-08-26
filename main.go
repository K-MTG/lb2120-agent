// lb2120-agent monitors a Netgear LB2120 LTE modem, pushes Prometheus
// metrics to a remote_write endpoint, and applies a rate-limited recovery
// (AT+CFUN=1) when it detects the modem's known "stuck in low power mode"
// firmware bug -- specifically distinguished from a genuine loss of
// coverage, which it only reports on and never tries to "fix".
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/K-MTG/lb2120-agent/internal/lb2120"
	"github.com/K-MTG/lb2120-agent/internal/metrics"
	"github.com/K-MTG/lb2120-agent/internal/remotewrite"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := parseConfig()
	if err != nil {
		slog.Error("bad config", "error", err)
		os.Exit(1)
	}

	if dir := filepath.Dir(cfg.StateFile); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Warn("could not create state dir, recovery state won't persist across restarts", "dir", dir, "error", err)
		}
	}

	recCfg := metrics.Config{
		ConfirmPolls:           cfg.ConfirmPolls,
		Cooldown:               cfg.Cooldown,
		MaxConsecutiveFailures: cfg.MaxFailures,
		BackoffDuration:        cfg.BackoffDuration,
	}
	state := metrics.LoadState(cfg.StateFile, recCfg)

	rwClient := remotewrite.New(cfg.RemoteWriteURL, map[string]string{
		"job":      cfg.Job,
		"instance": cfg.Instance,
	}, cfg.RemoteWriteTO)

	atAddr := fmt.Sprintf("%s:%d", cfg.DeviceIP, cfg.ATPort)
	webBase := fmt.Sprintf("http://%s", cfg.DeviceIP)

	slog.Info("lb2120-agent starting",
		"device_ip", cfg.DeviceIP,
		"poll_interval", cfg.PollInterval,
		"remote_write_url", cfg.RemoteWriteURL,
	)

	runOnce(ctx, cfg, atAddr, webBase, state, rwClient)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			runOnce(ctx, cfg, atAddr, webBase, state, rwClient)
		}
	}
	slog.Info("lb2120-agent shutting down")
}

func runOnce(ctx context.Context, cfg Config, atAddr, webBase string, state *metrics.State, rw *remotewrite.Client) {
	start := time.Now()
	log := slog.With("cycle_start", start.Format(time.RFC3339))

	atStatus, atErr := lb2120.QueryStatus(ctx, atAddr, cfg.ATTimeout)
	if atErr != nil {
		log.Error("AT status query failed", "error", atErr)
	}

	webClient, webErr := lb2120.NewWebClient(webBase, cfg.Password, cfg.WebTimeout)
	var model *lb2120.Model
	if webErr == nil {
		model, webErr = webClient.FetchModel(ctx)
	}
	if webErr != nil {
		log.Warn("web API fetch failed (metrics will be AT-only this cycle)", "error", webErr)
	}

	// The recovery trigger is deliberately AT-only and deliberately narrow:
	// CFUN==0 means the radio is administratively off. It does NOT trigger
	// on "no signal while the radio is on", which is what a genuine carrier
	// outage looks like -- that case is only ever reported via metrics.
	stuck := atErr == nil && atStatus.CFUN == 0

	decision := state.Evaluate(stuck, start)
	log.Info("recovery evaluation", "stuck", stuck, "attempt", decision.Attempt, "reason", decision.Reason, "in_backoff", decision.InBackoff)

	if decision.Attempt {
		if err := lb2120.Recover(ctx, atAddr, cfg.ATTimeout); err != nil {
			log.Error("recovery command failed to send", "error", err)
		} else {
			log.Warn("sent AT+CFUN=1 to recover stuck radio")
		}
	}

	if err := state.Save(cfg.StateFile); err != nil {
		log.Warn("could not persist recovery state", "error", err)
	}

	var atPtr *lb2120.ATStatus
	if atErr == nil {
		atPtr = &atStatus
	}

	snap := metrics.Snapshot{
		AT:                    atPtr,
		Model:                 model,
		Stuck:                 stuck,
		Decision:              decision,
		State:                 state,
		ScrapeDurationSeconds: time.Since(start).Seconds(),
		ATError:               atErr,
		WebError:              webErr,
	}
	samples := metrics.Build(snap)

	if err := rw.Push(ctx, samples, start); err != nil {
		log.Error("remote_write push failed", "error", err, "sample_count", len(samples))
		return
	}
	log.Info("cycle complete", "sample_count", len(samples), "duration", time.Since(start))
}
