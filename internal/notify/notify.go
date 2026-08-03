// Package notify delivers drill-completion notifications to
// operator-configured webhooks (docs/notifications.md). Notifications are
// observability, not evidence: a delivery failure is loud but never
// changes a drill's verdict, and the payload is a signpost to the signed
// evidence record, never a substitute for it.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/probavi/probavi/internal/config"
	"github.com/probavi/probavi/internal/evidence"
)

// Delivery constants (docs/notifications.md §3). Deliberately not
// configurable: tuning knobs would be new config keys, and config keys
// are one-way doors (AGENTS.md §5).
const (
	// Budget bounds total delivery time across all webhooks; the caller
	// derives the notification context from it, independent of the drill
	// timeout so cancelled drills still notify.
	Budget = 60 * time.Second

	// AttemptTimeout bounds one delivery attempt.
	AttemptTimeout = 10 * time.Second
	// Attempts is how many times one webhook is tried before giving up.
	Attempts = 3

	// maxDrain caps how much of a response body is read before closing;
	// receivers are untrusted and only the status code matters.
	maxDrain = 4 << 10
)

// Event is the only notification event of schema version 1.
const Event = "drill.completed"

// Request headers every delivery carries (docs/notifications.md §4).
const (
	// HeaderEvent names the event, so a receiver can route without
	// parsing the body.
	HeaderEvent = "X-Probavi-Event"
	// HeaderSignature carries the optional HMAC-SHA256 of the body,
	// GitHub-style as "sha256=<hex>".
	HeaderSignature = "X-Probavi-Signature-256"
	// ContentType is the body's media type.
	ContentType = "application/json"
	// SignatureAlgorithm names the MAC in the capabilities manifest.
	SignatureAlgorithm = "HMAC-SHA256"
)

// webhook is one resolved destination. The URL may have come from the
// environment and is treated as a credential everywhere: log lines and
// errors identify a webhook only by its config index.
type webhook struct {
	index  int
	url    string
	secret []byte
	on     map[evidence.Outcome]bool
}

// Notifier posts drill-completion payloads to the configured webhooks.
type Notifier struct {
	webhooks []webhook
	client   *http.Client
	logger   *slog.Logger
	version  string
	backoff  []time.Duration
}

// New resolves the notify config into a ready Notifier. Environment
// variables are read once, here, so a missing one aborts the run before
// the sandbox is created rather than after a long restore.
func New(cfg *config.Notify, version string, logger *slog.Logger) (*Notifier, error) {
	hooks := make([]webhook, 0, len(cfg.Webhooks))
	for i, w := range cfg.Webhooks {
		h := webhook{index: i, on: onSet(w.On)}
		h.url = w.URL
		if w.URLEnv != "" {
			v := os.Getenv(w.URLEnv)
			if v == "" {
				return nil, fmt.Errorf("notify.webhooks[%d]: environment variable %s is unset or empty", i, w.URLEnv)
			}
			h.url = v
		}
		// Literal URLs were validated by the config package; URLs from the
		// environment are seen first here. The error never echoes the value —
		// it may be a credential.
		if u, err := url.Parse(h.url); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("notify.webhooks[%d]: resolved url is not an absolute http(s) URL", i)
		}
		if w.SecretEnv != "" {
			s := os.Getenv(w.SecretEnv)
			if s == "" {
				return nil, fmt.Errorf("notify.webhooks[%d]: environment variable %s is unset or empty", i, w.SecretEnv)
			}
			h.secret = []byte(s)
		}
		hooks = append(hooks, h)
	}
	return &Notifier{
		webhooks: hooks,
		client: &http.Client{
			Timeout: AttemptTimeout,
			// Redirects are never followed: a redirect could hand a
			// token-bearing URL or signed body to an unintended host
			// (docs/notifications.md §3).
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger:  logger,
		version: version,
		backoff: []time.Duration{time.Second, 2 * time.Second},
	}, nil
}

// onSet expands the on filter; an absent filter means every outcome, so
// silence reliably signals "the drill did not run" to heartbeat receivers.
func onSet(on []string) map[evidence.Outcome]bool {
	set := make(map[evidence.Outcome]bool, len(on))
	if len(on) == 0 {
		return map[evidence.Outcome]bool{
			evidence.OutcomePass: true, evidence.OutcomeFail: true,
			evidence.OutcomeError: true, evidence.OutcomeCancelled: true,
		}
	}
	for _, o := range on {
		set[evidence.Outcome(o)] = true
	}
	return set
}

// Send posts the record's notification payload to every webhook whose on
// filter matches, sequentially in config order. Per-webhook failures are
// collected, never fatal to each other; the joined error is for logging
// only and must not influence the drill's exit code.
func (n *Notifier) Send(ctx context.Context, rec *evidence.Record) error {
	body, err := json.Marshal(NewPayload(rec))
	if err != nil {
		return fmt.Errorf("encode notification payload: %w", err)
	}
	var errs []error
	for _, h := range n.webhooks {
		if !h.on[rec.Outcome] {
			continue
		}
		if derr := n.deliver(ctx, h, body); derr != nil {
			errs = append(errs, fmt.Errorf("webhook[%d]: %w", h.index, derr))
			continue
		}
		n.logger.Info("notification delivered", "webhook", h.index, "outcome", rec.Outcome)
	}
	return errors.Join(errs...)
}

// deliver runs the attempt loop for one webhook: retries are for
// transport errors and 5xx only, with backoff that honors cancellation.
func (n *Notifier) deliver(ctx context.Context, h webhook, body []byte) error {
	var last error
	for attempt := range Attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("delivery aborted: %w", ctx.Err())
			case <-time.After(n.backoff[min(attempt-1, len(n.backoff)-1)]):
			}
		}
		retryable, err := n.post(ctx, h, body)
		if err == nil {
			return nil
		}
		last = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("after %d attempts: %w", Attempts, last)
}

// post makes one delivery attempt. Errors are redacted before they leave:
// Go's *url.Error embeds the full URL, which may be a credential, so only
// its inner transport error survives.
func (n *Notifier) post(ctx context.Context, h webhook, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		// Unreachable for a URL New validated; keep the message value-free
		// regardless.
		return false, errors.New("build request: invalid URL")
	}
	req.Header.Set("Content-Type", ContentType)
	req.Header.Set("User-Agent", "probavi/"+n.version)
	req.Header.Set(HeaderEvent, Event)
	if h.secret != nil {
		mac := hmac.New(sha256.New, h.secret)
		if _, werr := mac.Write(body); werr != nil {
			return false, fmt.Errorf("sign payload: %w", werr)
		}
		req.Header.Set(HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := n.client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return true, fmt.Errorf("post: %w", err)
	}
	if _, derr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain)); derr != nil {
		n.logger.Debug("drain notification response", "webhook", h.index, "err", derr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		n.logger.Debug("close notification response", "webhook", h.index, "err", cerr)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("response status %d", resp.StatusCode)
	default:
		// 3xx (redirects are never followed) and 4xx are configuration
		// problems a retry cannot fix.
		return false, fmt.Errorf("response status %d", resp.StatusCode)
	}
}
