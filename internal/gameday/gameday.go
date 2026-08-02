// Package gameday orchestrates DR game-days: member restore drills
// executed in dependency order (docs/gameday.md). The package owns
// ordering, bounded parallelism, skip cascades, and summary aggregation;
// executing one member drill is injected by the caller, so the
// orchestration logic is fully testable without sandboxes.
package gameday

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aafeher/probavi/internal/config"
	"github.com/aafeher/probavi/internal/evidence"
)

// OutcomeSkipped is the member outcome for drills that never ran because
// a dependency did not pass or the game-day was cancelled. A skipped
// member appends no evidence record — nothing was proven about it.
const OutcomeSkipped = "skipped"

// Summary-level error codes for member failures that happen outside an
// evidence record (docs/gameday.md §5); record-level codes come from the
// drill itself.
const (
	// ErrCodeSetup marks a member that could not be wired at all.
	ErrCodeSetup = "setup_error"
	// ErrCodeEvidenceLost marks a member whose drill ran but whose record
	// could not be written — the most serious way a drill can end.
	ErrCodeEvidenceLost = "evidence_lost"
)

// DrillSummary is the machine-readable summary of one drill run. It is
// the exact one-line output shape of `probavi run`; the CLI aliases it so
// the standalone and game-day member summaries cannot drift apart.
type DrillSummary struct {
	Outcome      string `json:"outcome"`
	Seq          int64  `json:"seq"`
	EvidencePath string `json:"evidence_path"`
	ChecksPassed int    `json:"checks_passed"`
	ChecksTotal  int    `json:"checks_total"`
	RestoreMS    *int64 `json:"restore_ms"`
	TotalMS      *int64 `json:"total_ms"`
	ErrorCode    string `json:"error_code,omitempty"`
}

// MemberResult is one member's line in the game-day summary. For members
// that ran, the embedded DrillSummary carries the drill's own summary
// (its Outcome field is shadowed by the member-level one, which always
// equals it); for skipped members it stays nil and only Name, Outcome,
// SkipReason, and DurationMS appear.
type MemberResult struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	*DrillSummary
	SkipReason string `json:"skip_reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Summary is the game-day's machine-readable result (docs/gameday.md §5).
// Members appear in file order regardless of execution order.
type Summary struct {
	GameDay    string         `json:"gameday"`
	ConfigHash string         `json:"config_hash"`
	Outcome    string         `json:"outcome"`
	Members    []MemberResult `json:"members"`
	TotalMS    int64          `json:"total_ms"`
}

// RunFunc executes one member's drill under ctx and reports its summary.
// Implementations map wiring and evidence-persistence failures to outcome
// "error" with ErrCodeSetup or ErrCodeEvidenceLost.
type RunFunc func(ctx context.Context, member config.GameDayMember) DrillSummary

// memberDone carries one finished member from its goroutine back to the
// scheduler loop, which owns all shared state.
type memberDone struct {
	index    int
	summary  DrillSummary
	duration time.Duration
}

// Run executes the game-day: members start when all their dependencies
// passed, at most cfg.Parallelism() at a time, in file order among ready
// members. A member whose dependency did not pass is skipped (cascading);
// cancellation skips everything not yet started and waits out running
// members, whose own drill contexts derive from ctx.
func Run(ctx context.Context, cfg *config.GameDay, run RunFunc, logger *slog.Logger) *Summary {
	start := time.Now()
	results := make([]MemberResult, len(cfg.Members))
	outcomes := make(map[string]string, len(cfg.Members))
	pending := make([]bool, len(cfg.Members))
	for i := range cfg.Members {
		results[i] = MemberResult{Name: cfg.Members[i].Name}
		pending[i] = true
	}
	done := make(chan memberDone)
	running := 0
	for {
		skipBlockedMembers(cfg, pending, outcomes, results, logger)
		if ctx.Err() != nil {
			skipRemaining(cfg, pending, outcomes, results, logger)
		}
		running += launchReady(ctx, cfg, pending, outcomes, done, run, logger, cfg.Parallelism()-running)
		if running == 0 {
			break
		}
		d := <-done
		running--
		results[d.index].Outcome = d.summary.Outcome
		results[d.index].DrillSummary = &d.summary
		results[d.index].DurationMS = d.duration.Milliseconds()
		outcomes[cfg.Members[d.index].Name] = d.summary.Outcome
		logger.Info("member drill finished", "member", cfg.Members[d.index].Name,
			"outcome", d.summary.Outcome, "duration_ms", d.duration.Milliseconds())
	}
	return &Summary{
		GameDay:    cfg.Name,
		ConfigHash: cfg.Hash,
		Outcome:    overallOutcome(results),
		Members:    results,
		TotalMS:    time.Since(start).Milliseconds(),
	}
}

// skipBlockedMembers marks every pending member with a finished non-pass
// dependency as skipped, repeating until the cascade settles.
func skipBlockedMembers(cfg *config.GameDay, pending []bool, outcomes map[string]string, results []MemberResult, logger *slog.Logger) {
	for changed := true; changed; {
		changed = false
		for i := range cfg.Members {
			if !pending[i] {
				continue
			}
			dep, outcome := blockedBy(&cfg.Members[i], outcomes)
			if dep == "" {
				continue
			}
			pending[i] = false
			changed = true
			reason := fmt.Sprintf("dependency %s did not pass (%s)", dep, outcome)
			results[i].Outcome = OutcomeSkipped
			results[i].SkipReason = reason
			outcomes[cfg.Members[i].Name] = OutcomeSkipped
			logger.Info("member skipped", "member", cfg.Members[i].Name, "reason", reason)
		}
	}
}

// blockedBy returns the first dependency (in declaration order) that
// finished with a non-pass outcome, or "" when none has.
func blockedBy(m *config.GameDayMember, outcomes map[string]string) (dep, outcome string) {
	for _, d := range m.DependsOn {
		if o, finished := outcomes[d]; finished && o != string(evidence.OutcomePass) {
			return d, o
		}
	}
	return "", ""
}

// skipRemaining marks every still-pending member as skipped after
// cancellation or game-day timeout.
func skipRemaining(cfg *config.GameDay, pending []bool, outcomes map[string]string, results []MemberResult, logger *slog.Logger) {
	for i := range cfg.Members {
		if !pending[i] {
			continue
		}
		pending[i] = false
		results[i].Outcome = OutcomeSkipped
		results[i].SkipReason = "game-day cancelled"
		outcomes[cfg.Members[i].Name] = OutcomeSkipped
		logger.Info("member skipped", "member", cfg.Members[i].Name, "reason", "game-day cancelled")
	}
}

// launchReady starts up to slots ready members (all dependencies passed)
// in file order and returns how many it started.
func launchReady(ctx context.Context, cfg *config.GameDay, pending []bool, outcomes map[string]string, done chan<- memberDone, run RunFunc, logger *slog.Logger, slots int) int {
	launched := 0
	for i := range cfg.Members {
		if launched >= slots {
			break
		}
		if !pending[i] || !ready(&cfg.Members[i], outcomes) {
			continue
		}
		pending[i] = false
		launched++
		logger.Info("member drill starting", "member", cfg.Members[i].Name)
		go func(index int, member config.GameDayMember) {
			began := time.Now()
			summary := run(ctx, member)
			done <- memberDone{index: index, summary: summary, duration: time.Since(began)}
		}(i, cfg.Members[i])
	}
	return launched
}

func ready(m *config.GameDayMember, outcomes map[string]string) bool {
	for _, dep := range m.DependsOn {
		if outcomes[dep] != string(evidence.OutcomePass) {
			return false
		}
	}
	return true
}

// overallOutcome aggregates member outcomes (docs/gameday.md §5): any
// drill failure makes the exercise a failure; otherwise anything short of
// all-pass — errors, cancellations, and the skips they caused — is an
// infrastructure error, not a recoverability verdict.
func overallOutcome(results []MemberResult) string {
	anyFail := false
	allPass := true
	for i := range results {
		switch results[i].Outcome {
		case string(evidence.OutcomePass):
		case string(evidence.OutcomeFail):
			anyFail = true
			allPass = false
		default:
			allPass = false
		}
	}
	switch {
	case allPass:
		return string(evidence.OutcomePass)
	case anyFail:
		return string(evidence.OutcomeFail)
	default:
		return string(evidence.OutcomeError)
	}
}
