package gameday

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aafeher/probavi/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func member(name string, deps ...string) config.GameDayMember {
	return config.GameDayMember{Name: name, Config: name + ".yaml", DependsOn: deps}
}

func gdConfig(maxParallel int, members ...config.GameDayMember) *config.GameDay {
	return &config.GameDay{
		Name:        "gd-test",
		Hash:        "sha256:" + strings.Repeat("ab", 32),
		MaxParallel: maxParallel,
		Members:     members,
	}
}

func passSummary(seq int64) DrillSummary {
	return DrillSummary{Outcome: "pass", Seq: seq, EvidencePath: "/e.jsonl", ChecksPassed: 1, ChecksTotal: 1}
}

// orderedRunner records invocation order and returns per-member summaries
// (pass when a member has no entry).
type orderedRunner struct {
	mu      sync.Mutex
	order   []string
	results map[string]DrillSummary
}

func (r *orderedRunner) run(_ context.Context, m config.GameDayMember) DrillSummary {
	r.mu.Lock()
	r.order = append(r.order, m.Name)
	seq := int64(len(r.order))
	r.mu.Unlock()
	if s, ok := r.results[m.Name]; ok {
		return s
	}
	return passSummary(seq)
}

func (r *orderedRunner) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func TestRunSequentialChain(t *testing.T) {
	r := &orderedRunner{}
	cfg := gdConfig(0, member("alpha"), member("beta", "alpha"), member("gamma", "beta"))
	s := Run(context.Background(), cfg, r.run, discardLogger())

	if got := strings.Join(r.ran(), ","); got != "alpha,beta,gamma" {
		t.Errorf("execution order = %s, want dependency order alpha,beta,gamma", got)
	}
	if s.Outcome != "pass" || s.GameDay != "gd-test" || s.ConfigHash != cfg.Hash {
		t.Errorf("summary = %+v, want pass with config identity", s)
	}
	if len(s.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(s.Members))
	}
	for i, m := range s.Members {
		if m.Name != cfg.Members[i].Name {
			t.Errorf("members[%d] = %s, want file order %s", i, m.Name, cfg.Members[i].Name)
		}
		if m.Outcome != "pass" || m.DrillSummary == nil || m.DrillSummary.Outcome != "pass" {
			t.Errorf("members[%d] = %+v, want pass with drill summary", i, m)
		}
	}
	if s.TotalMS < 0 {
		t.Errorf("TotalMS = %d, want >= 0", s.TotalMS)
	}
}

func TestRunReadyMembersStartInFileOrder(t *testing.T) {
	r := &orderedRunner{}
	// beta and gamma both become ready when alpha passes; file order wins.
	cfg := gdConfig(1, member("alpha"), member("beta", "alpha"), member("gamma", "alpha"))
	Run(context.Background(), cfg, r.run, discardLogger())
	if got := strings.Join(r.ran(), ","); got != "alpha,beta,gamma" {
		t.Errorf("execution order = %s, want alpha,beta,gamma", got)
	}
}

func TestRunFailureSkipsDependentsTransitively(t *testing.T) {
	r := &orderedRunner{results: map[string]DrillSummary{
		"alpha": {Outcome: "fail", Seq: 1, EvidencePath: "/e.jsonl", ErrorCode: "check_failed"},
	}}
	cfg := gdConfig(0,
		member("alpha"),
		member("beta", "alpha"),
		member("gamma", "beta"),
		member("delta"),
	)
	s := Run(context.Background(), cfg, r.run, discardLogger())

	if got := strings.Join(r.ran(), ","); got != "alpha,delta" {
		t.Errorf("ran = %s, want alpha,delta only", got)
	}
	if s.Outcome != "fail" {
		t.Errorf("outcome = %s, want fail", s.Outcome)
	}
	beta, gamma := s.Members[1], s.Members[2]
	if beta.Outcome != OutcomeSkipped || beta.SkipReason != "dependency alpha did not pass (fail)" {
		t.Errorf("beta = %+v, want skipped naming alpha", beta)
	}
	if gamma.Outcome != OutcomeSkipped || gamma.SkipReason != "dependency beta did not pass (skipped)" {
		t.Errorf("gamma = %+v, want skipped cascading from beta", gamma)
	}
	if beta.DrillSummary != nil || gamma.DrillSummary != nil {
		t.Error("skipped members must carry no drill summary")
	}
	if s.Members[3].Outcome != "pass" {
		t.Errorf("independent delta = %+v, want pass", s.Members[3])
	}
}

func TestRunErrorWithoutFailureIsError(t *testing.T) {
	r := &orderedRunner{results: map[string]DrillSummary{
		"alpha": {Outcome: "error", ErrorCode: ErrCodeSetup},
	}}
	cfg := gdConfig(0, member("alpha"), member("beta", "alpha"))
	s := Run(context.Background(), cfg, r.run, discardLogger())
	if s.Outcome != "error" {
		t.Errorf("outcome = %s, want error (no drill failed)", s.Outcome)
	}
	if s.Members[1].Outcome != OutcomeSkipped ||
		s.Members[1].SkipReason != "dependency alpha did not pass (error)" {
		t.Errorf("beta = %+v, want skipped behind the error", s.Members[1])
	}
}

func TestRunHonorsMaxParallel(t *testing.T) {
	var mu sync.Mutex
	current, peak := 0, 0
	runner := func(context.Context, config.GameDayMember) DrillSummary {
		mu.Lock()
		current++
		if current > peak {
			peak = current
		}
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()
		return passSummary(1)
	}
	cfg := gdConfig(2, member("a"), member("b"), member("c"), member("d"))
	s := Run(context.Background(), cfg, runner, discardLogger())
	if s.Outcome != "pass" {
		t.Fatalf("outcome = %s, want pass", s.Outcome)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != 2 {
		t.Errorf("peak concurrency = %d, want exactly max_parallel = 2", peak)
	}
}

func TestRunCancellationSkipsPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := func(context.Context, config.GameDayMember) DrillSummary {
		// The first (and only started) member observes the game-day being
		// cancelled mid-drill and reports a cancelled record.
		cancel()
		return DrillSummary{Outcome: "cancelled", Seq: 1, EvidencePath: "/e.jsonl", ErrorCode: "cancelled"}
	}
	cfg := gdConfig(1, member("alpha"), member("beta"))
	s := Run(ctx, cfg, runner, discardLogger())

	if s.Members[0].Outcome != "cancelled" {
		t.Errorf("alpha = %+v, want cancelled", s.Members[0])
	}
	if s.Members[1].Outcome != OutcomeSkipped || s.Members[1].SkipReason != "game-day cancelled" {
		t.Errorf("beta = %+v, want skipped by cancellation", s.Members[1])
	}
	if s.Outcome != "error" {
		t.Errorf("outcome = %s, want error", s.Outcome)
	}
}

// TestMemberResultJSONShape pins the docs/gameday.md §5 serialization:
// skipped members carry no drill-summary fields; ran members flatten the
// drill summary with the member-level outcome winning the name clash.
func TestMemberResultJSONShape(t *testing.T) {
	skipped, err := json.Marshal(MemberResult{
		Name: "x", Outcome: OutcomeSkipped, SkipReason: "dependency a did not pass (fail)",
	})
	if err != nil {
		t.Fatalf("marshal skipped: %v", err)
	}
	wantSkipped := `{"name":"x","outcome":"skipped","skip_reason":"dependency a did not pass (fail)","duration_ms":0}`
	if string(skipped) != wantSkipped {
		t.Errorf("skipped JSON:\n got %s\nwant %s", skipped, wantSkipped)
	}

	restore := int64(190)
	ran, err := json.Marshal(MemberResult{
		Name: "y", Outcome: "fail",
		DrillSummary: &DrillSummary{
			Outcome: "fail", Seq: 4, EvidencePath: "/e.jsonl",
			ChecksPassed: 1, ChecksTotal: 3, RestoreMS: &restore, ErrorCode: "check_failed",
		},
		DurationMS: 7,
	})
	if err != nil {
		t.Fatalf("marshal ran: %v", err)
	}
	wantRan := `{"name":"y","outcome":"fail","seq":4,"evidence_path":"/e.jsonl","checks_passed":1,"checks_total":3,"restore_ms":190,"total_ms":null,"error_code":"check_failed","duration_ms":7}`
	if string(ran) != wantRan {
		t.Errorf("ran JSON:\n got %s\nwant %s", ran, wantRan)
	}
}
