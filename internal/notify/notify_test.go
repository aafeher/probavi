package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/probavi/probavi/internal/config"
	"github.com/probavi/probavi/internal/evidence"
)

const configHash = "sha256:9f2a11a6a9e1a76f7e4c62b9b2b0a3f2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6"

func i64(v int64) *int64 { return &v }

// passRecord, failRecord, and errorRecord mirror the three golden payload
// lines; the golden file freezes their serialized bytes.
func passRecord() *evidence.Record {
	return &evidence.Record{
		Schema:  "probavi-evidence/1",
		Seq:     7,
		TS:      "2026-08-01T03:10:02.481Z",
		Drill:   evidence.Drill{Name: "prod-orders-db", ConfigHash: configHash},
		Adapter: evidence.Adapter{Name: "postgres", Protocol: "probavi-adapter/0"},
		Timings: evidence.Timings{Restore: i64(190), Total: i64(2412)},
		Checks: []evidence.Check{
			{Name: "service_healthy", OK: true},
			{Name: "table_exists:orders", OK: true},
			{Name: "row_count:orders", OK: true},
		},
		Outcome: evidence.OutcomePass,
		Env:     evidence.Env{ProbaviVersion: "0.1.0"},
	}
}

func failRecord() *evidence.Record {
	rec := passRecord()
	rec.Seq = 8
	rec.TS = "2026-08-01T04:00:00.000Z"
	rec.Timings = evidence.Timings{Restore: i64(210), Total: i64(2500)}
	rec.Checks[2].OK = false
	rec.Outcome = evidence.OutcomeFail
	rec.Error = &evidence.DrillError{Code: "check_failed", Message: "1 of 3 checks failed"}
	return rec
}

func errorRecord() *evidence.Record {
	rec := passRecord()
	rec.Seq = 9
	rec.TS = "2026-08-01T05:00:00.000Z"
	rec.Timings = evidence.Timings{}
	rec.Checks = nil
	rec.Outcome = evidence.OutcomeError
	rec.Error = &evidence.DrillError{Code: "sandbox_error", Message: "sandbox create failed"}
	return rec
}

// TestNewPayloadGolden freezes the payload bytes: docs/notifications.md §5
// and docs/schemas/notification/payload.json describe exactly this form,
// and internal/spec validates the same golden against the schema.
func TestNewPayloadGolden(t *testing.T) {
	records := []*evidence.Record{passRecord(), failRecord(), errorRecord()}
	golden, err := os.ReadFile("testdata/payload.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(golden), "\n"), "\n")
	if len(lines) != len(records) {
		t.Fatalf("golden has %d lines, want %d", len(lines), len(records))
	}
	for i, rec := range records {
		got, err := json.Marshal(NewPayload(rec))
		if err != nil {
			t.Fatalf("marshal payload %d: %v", i, err)
		}
		if string(got) != lines[i] {
			t.Errorf("payload %d:\n got %s\nwant %s", i, got, lines[i])
		}
	}
}

// capture is a race-safe recorder for requests a test server received.
type capture struct {
	mu   sync.Mutex
	reqs []capturedRequest
}

type capturedRequest struct {
	header http.Header
	body   []byte
}

func (c *capture) add(t *testing.T, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, capturedRequest{header: r.Header.Clone(), body: body})
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func (c *capture) request(t *testing.T, i int) capturedRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.reqs) {
		t.Fatalf("request %d not captured (have %d)", i, len(c.reqs))
	}
	return c.reqs[i]
}

// newNotifier builds a Notifier with test-speed backoff and a captured
// log stream.
func newNotifier(t *testing.T, cfg *config.Notify, logs *bytes.Buffer) *Notifier {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	n, err := New(cfg, "0.1.0-test", logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	n.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return n
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewResolvesEnvironment(t *testing.T) {
	t.Setenv("PROBAVI_TEST_HOOK_URL", "https://example.internal/hook")
	cfg := &config.Notify{Webhooks: []config.NotifyWebhook{
		{URL: "https://example.internal/hook"},
		{URLEnv: "PROBAVI_TEST_HOOK_URL"},
	}}
	if _, err := New(cfg, "0.1.0", discardLogger()); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestNewRejectsUnresolvableWebhooks(t *testing.T) {
	tests := []struct {
		name    string
		webhook config.NotifyWebhook
		env     map[string]string
		wantErr []string // substrings the error must contain
		leak    string   // must never appear in the error
	}{
		{
			name:    "unset url env",
			webhook: config.NotifyWebhook{URLEnv: "PROBAVI_TEST_HOOK_UNSET"},
			wantErr: []string{"notify.webhooks[0]", "PROBAVI_TEST_HOOK_UNSET", "unset or empty"},
		},
		{
			name:    "env url not http",
			webhook: config.NotifyWebhook{URLEnv: "PROBAVI_TEST_HOOK_URL"},
			env:     map[string]string{"PROBAVI_TEST_HOOK_URL": "ftp://secret-token@example.internal"},
			wantErr: []string{"notify.webhooks[0]", "not an absolute http(s) URL"},
			leak:    "secret-token",
		},
		{
			name: "unset secret env",
			webhook: config.NotifyWebhook{
				URL:       "https://example.internal/hook",
				SecretEnv: "PROBAVI_TEST_SECRET_UNSET",
			},
			wantErr: []string{"notify.webhooks[0]", "PROBAVI_TEST_SECRET_UNSET", "unset or empty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := New(&config.Notify{Webhooks: []config.NotifyWebhook{tt.webhook}}, "0.1.0", discardLogger())
			if err == nil {
				t.Fatal("New accepted an unresolvable webhook")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if tt.leak != "" && strings.Contains(err.Error(), tt.leak) {
				t.Errorf("error %q leaks the URL value", err)
			}
		})
	}
}

// TestSendFiltersAndDelivers proves the on filter, the request shape, and
// that neither logs nor errors ever carry the webhook URL.
func TestSendFiltersAndDelivers(t *testing.T) {
	caught := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caught.add(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{
		{URL: srv.URL + "/fail-only", On: []string{"fail", "error"}},
		{URL: srv.URL + "/all"},
	}}, &logs)

	rec := passRecord()
	if err := n.Send(context.Background(), rec); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := caught.count(); got != 1 {
		t.Fatalf("delivered %d requests, want 1 (pass must not match on:[fail,error])", got)
	}
	req := caught.request(t, 0)
	wantBody, err := json.Marshal(NewPayload(rec))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(req.body, wantBody) {
		t.Errorf("body = %s, want %s", req.body, wantBody)
	}
	for header, want := range map[string]string{
		"Content-Type":    "application/json",
		"User-Agent":      "probavi/0.1.0-test",
		"X-Probavi-Event": "drill.completed",
	} {
		if got := req.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := req.header.Get("X-Probavi-Signature-256"); got != "" {
		t.Errorf("unsigned webhook carries signature header %q", got)
	}
	if strings.Contains(logs.String(), srv.URL) {
		t.Errorf("logs leak the webhook URL:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "notification delivered") {
		t.Errorf("success was not logged:\n%s", logs.String())
	}
}

func TestSendSignsPayload(t *testing.T) {
	caught := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caught.add(t, r)
	}))
	defer srv.Close()

	t.Setenv("PROBAVI_TEST_HOOK_SECRET", "shhh")
	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{
		{URL: srv.URL, SecretEnv: "PROBAVI_TEST_HOOK_SECRET"},
	}}, &logs)

	if err := n.Send(context.Background(), passRecord()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := caught.request(t, 0)
	mac := hmac.New(sha256.New, []byte("shhh"))
	if _, err := mac.Write(req.body); err != nil {
		t.Fatalf("hmac: %v", err)
	}
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := req.header.Get("X-Probavi-Signature-256"); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

func TestSendRetriesServerErrors(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{{URL: srv.URL}}}, &logs)
	if err := n.Send(context.Background(), passRecord()); err != nil {
		t.Fatalf("Send after one 503: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("server saw %d requests, want 2 (one retry)", calls)
	}
}

func TestSendClientErrorIsPermanent(t *testing.T) {
	caught := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caught.add(t, r)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{{URL: srv.URL}}}, &logs)
	err := n.Send(context.Background(), passRecord())
	if err == nil {
		t.Fatal("Send succeeded against a 404 endpoint")
	}
	for _, want := range []string{"webhook[0]", "status 404"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if got := caught.count(); got != 1 {
		t.Errorf("server saw %d requests, want 1 (4xx must not be retried)", got)
	}
}

func TestSendNeverFollowsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("redirect target was contacted")
	}))
	defer target.Close()
	caught := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caught.add(t, r)
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{{URL: srv.URL}}}, &logs)
	err := n.Send(context.Background(), passRecord())
	if err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Fatalf("Send = %v, want permanent status 302 failure", err)
	}
	if got := caught.count(); got != 1 {
		t.Errorf("server saw %d requests, want 1", got)
	}
}

// TestSendRedactsTransportErrors plants a token in the URL path and takes
// the server down: the joined error must describe the failure without
// echoing the URL.
func TestSendRedactsTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/services/SECRETTOKEN"
	srv.Close() // every attempt now fails at the transport layer

	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{{URL: url}}}, &logs)
	err := n.Send(context.Background(), passRecord())
	if err == nil {
		t.Fatal("Send succeeded against a closed server")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error %q should report exhausted attempts", err)
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("error %q leaks the URL path", err)
	}
	if strings.Contains(logs.String(), "SECRETTOKEN") {
		t.Errorf("logs leak the URL path:\n%s", logs.String())
	}
}

func TestSendHonorsCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	n := newNotifier(t, &config.Notify{Webhooks: []config.NotifyWebhook{{URL: srv.URL}}}, &logs)
	n.backoff = []time.Duration{time.Hour} // only cancellation can end the wait
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Send(ctx, passRecord()) }()
	time.Sleep(50 * time.Millisecond) // let the first attempt fail and enter backoff
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "delivery aborted") {
			t.Errorf("Send = %v, want delivery aborted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancellation")
	}
}
