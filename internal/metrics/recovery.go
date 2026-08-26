// Package metrics holds the recovery decision state machine. It is kept
// separate from metric encoding because its whole job is a safety policy:
// decide, on each poll, whether the agent should send AT+CFUN=1, without
// ever reacting to a transient blip or hammering the modem indefinitely.
package metrics

import (
	"encoding/json"
	"os"
	"time"
)

// Config tunes the recovery policy.
type Config struct {
	// ConfirmPolls is how many consecutive stuck observations are required
	// before acting, so a normal boot sequence isn't mistaken for the bug.
	ConfirmPolls int
	// Cooldown is the minimum time between recovery attempts.
	Cooldown time.Duration
	// MaxConsecutiveFailures is how many attempts in a row can fail to
	// clear the stuck state before the agent stops trying and enters
	// backoff (surfaced via a metric for alerting, rather than retried
	// forever).
	MaxConsecutiveFailures int
	// BackoffDuration is how long the agent waits, once in backoff, before
	// it will try again.
	BackoffDuration time.Duration
}

// State is the recovery state machine's persisted state. It's small enough
// to serialize as-is; persisting it means a service restart mid-backoff
// doesn't forget the failure streak and start hammering the modem again.
type State struct {
	Cfg Config `json:"-"`

	ConsecutiveStuck          int       `json:"consecutive_stuck"`
	StuckSince                time.Time `json:"stuck_since"`
	LastAttempt               time.Time `json:"last_attempt"`
	ConsecutiveFailedAttempts int       `json:"consecutive_failed_attempts"`
	BackoffUntil              time.Time `json:"backoff_until"`
	AttemptsTotal             int64     `json:"attempts_total"`
	SuccessesTotal            int64     `json:"successes_total"`
	FailuresTotal             int64     `json:"failures_total"`

	// awaitingResult is true between "we just attempted recovery" and "we
	// evaluated whether it worked on the next poll".
	awaitingResult bool
}

func LoadState(path string, cfg Config) *State {
	s := &State{Cfg: cfg}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, s) // best-effort; zero value is a safe fallback
	s.Cfg = cfg
	return s
}

func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Decision is the outcome of evaluating one poll's stuck/not-stuck reading.
type Decision struct {
	Attempt   bool
	Reason    string // human-readable, for logging
	InBackoff bool
}

// Evaluate updates the state machine with this poll's stuck reading and
// returns whether the agent should attempt AT+CFUN=1 now.
//
// Call RecordOutcome on the *next* poll once you know whether the previous
// attempt actually cleared the stuck condition.
func (s *State) Evaluate(stuckNow bool, now time.Time) Decision {
	if s.awaitingResult {
		s.recordOutcome(!stuckNow, now)
	}

	if !stuckNow {
		s.ConsecutiveStuck = 0
		s.StuckSince = time.Time{}
		return Decision{Attempt: false, Reason: "not stuck"}
	}

	if s.StuckSince.IsZero() {
		s.StuckSince = now
	}
	s.ConsecutiveStuck++

	if now.Before(s.BackoffUntil) {
		return Decision{Attempt: false, Reason: "in backoff after repeated failed attempts", InBackoff: true}
	}

	if s.ConsecutiveStuck < s.Cfg.ConfirmPolls {
		return Decision{Attempt: false, Reason: "stuck but not yet confirmed across enough polls"}
	}

	if !s.LastAttempt.IsZero() && now.Sub(s.LastAttempt) < s.Cfg.Cooldown {
		return Decision{Attempt: false, Reason: "stuck but within cooldown of last attempt"}
	}

	s.LastAttempt = now
	s.AttemptsTotal++
	s.awaitingResult = true
	return Decision{Attempt: true, Reason: "stuck, confirmed, cooldown elapsed: attempting recovery"}
}

// recordOutcome is called internally, one poll after an attempt, using
// whether the modem is no longer stuck as the signal for success.
func (s *State) recordOutcome(succeeded bool, now time.Time) {
	s.awaitingResult = false
	if succeeded {
		s.SuccessesTotal++
		s.ConsecutiveFailedAttempts = 0
		return
	}

	s.FailuresTotal++
	s.ConsecutiveFailedAttempts++
	if s.ConsecutiveFailedAttempts >= s.Cfg.MaxConsecutiveFailures {
		s.BackoffUntil = now.Add(s.Cfg.BackoffDuration)
		s.ConsecutiveFailedAttempts = 0
	}
}
