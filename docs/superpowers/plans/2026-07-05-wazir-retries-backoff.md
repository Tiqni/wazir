# Retries & Backoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retry transient GitHub/git/`claude`-transport failures with bounded exponential backoff so a momentary blip no longer sends a card to `Failed`, while deterministic model-reported failures still fail exactly as today.

**Architecture:** A new leaf `internal/retry` package (Policy + `Do` + `Backoff`). GitHub HTTP (REST + GraphQL + forge PRs) retries in one retrying `http.RoundTripper` wrapping the shared ghinstallation transport in `internal/githubauth`. Git retries at the single `gitRunner.run` network chokepoint. `claude` retries conservatively around the `exec` run in `internal/claude`. The `internal/orchestrator` core is untouched.

**Tech Stack:** Go 1.25, `go.uber.org/zap`, `kkyr/fig`, `google/go-github/v66`, `shurcooL/githubv4`, `bradleyfalzon/ghinstallation/v2`, stdlib `net/http` / `os/exec`.

## Global Constraints

- Go directive is `1.25.0`; `go vet ./...` is the lint (no golangci config, no Makefile).
- `go test ./...` runs with **no network and no credentials** — every new test must too.
- CLAUDE.md rule 1: `internal/orchestrator` imports only `internal/board` + `internal/forge` interfaces, never a provider package. `internal/orchestrator/imports_test.go` must stay green. `internal/retry` is a leaf (no provider imports); it is imported only by provider-side packages (`githubauth`, `forge/github`, `claude`).
- Module path: `github.com/EmadMokhtar/wazir`.
- Commit after every task with a Conventional-Commits message; end each commit body with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Deterministic model-reported failures (`is_error`, a non-`success` subtype, `res.Status != complete`) are **never** retried. Only *transport*/network failures are.

---

### Task 1: `internal/retry` leaf helper

**Files:**
- Create: `internal/retry/retry.go`
- Test: `internal/retry/retry_test.go`

**Interfaces:**
- Produces:
  - `type Policy struct { MaxAttempts int; BaseDelay time.Duration; MaxDelay time.Duration }`
  - `type Classifier func(err error) (retry bool, retryAfter time.Duration)`
  - `func Do(ctx context.Context, p Policy, classify Classifier, fn func() error) error`
  - `func Backoff(p Policy, attempt int) time.Duration` — jittered delay before the `(attempt+1)`-th try; `attempt` is 1-based.
  - package var `jitter func(time.Duration) time.Duration` (unexported; overridable by in-package tests).

- [ ] **Step 1: Write the failing test**

`internal/retry/retry_test.go`:

```go
package retry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// always classifies every error as retryable with no extra delay.
func always(error) (bool, time.Duration) { return true, 0 }

func TestDoSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 4}, always, func() error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want nil/1", err, calls)
	}
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	old := jitter
	jitter = func(time.Duration) time.Duration { return 0 } // no real sleeping
	defer func() { jitter = old }()

	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		always, func() error {
			calls++
			if calls < 3 {
				return errors.New("boom")
			}
			return nil
		})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want nil/3", err, calls)
	}
}

func TestDoExhausts(t *testing.T) {
	old := jitter
	jitter = func(time.Duration) time.Duration { return 0 }
	defer func() { jitter = old }()

	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		always, func() error { calls++; return errors.New("boom") })
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
	if err == nil || !strings.Contains(err.Error(), "gave up after 3") {
		t.Fatalf("want a give-up error mentioning 3 attempts, got %v", err)
	}
}

func TestDoStopsWhenClassifierDeclines(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		func(error) (bool, time.Duration) { return false, 0 },
		func() error { calls++; return errors.New("permanent") })
	if calls != 1 || err == nil || err.Error() != "permanent" {
		t.Fatalf("calls=%d err=%v, want 1/permanent (unwrapped)", calls, err)
	}
}

func TestDoHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done, so the backoff select takes the ctx branch
	calls := 0
	err := Do(ctx, Policy{MaxAttempts: 4, BaseDelay: 10 * time.Second, MaxDelay: time.Minute},
		always, func() error { calls++; return errors.New("boom") })
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d err=%v, want 1 call and a Canceled error", calls, err)
	}
}

func TestBackoffCapsAndZeroes(t *testing.T) {
	old := jitter
	jitter = func(d time.Duration) time.Duration { return d } // identity: return the pre-jitter value
	defer func() { jitter = old }()

	p := Policy{BaseDelay: time.Second, MaxDelay: 4 * time.Second}
	if got := Backoff(p, 1); got != time.Second {
		t.Errorf("attempt 1: got %v want 1s", got)
	}
	if got := Backoff(p, 2); got != 2*time.Second {
		t.Errorf("attempt 2: got %v want 2s", got)
	}
	if got := Backoff(p, 10); got != 4*time.Second { // capped at MaxDelay
		t.Errorf("attempt 10: got %v want 4s (cap)", got)
	}
	if got := Backoff(Policy{}, 1); got != 0 { // zero BaseDelay => 0
		t.Errorf("zero policy: got %v want 0", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `env -u GOROOT go test ./internal/retry/ -run . -v`
Expected: FAIL — `undefined: Do`, `undefined: Policy`, `undefined: Backoff`, `undefined: jitter`.

- [ ] **Step 3: Write the implementation**

`internal/retry/retry.go`:

```go
// Package retry provides bounded exponential-backoff retries for transient
// failures. It is a leaf utility with no provider imports, so packages that use
// it keep the orchestrator core provider-agnostic (CLAUDE.md rule 1).
package retry

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// Policy configures Do/Backoff. MaxAttempts is the total number of tries
// including the first, so a value <= 1 means "run once, never retry".
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Classifier reports whether err is worth retrying and, optionally, a minimum
// delay before the next attempt (e.g. a rate-limit reset). A retryAfter <= 0
// means "use the computed backoff".
type Classifier func(err error) (retry bool, retryAfter time.Duration)

// jitter draws a delay in [0, d]. A package var so tests can pin it.
var jitter = func(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)))
}

// Do runs fn until it returns nil, the classifier declines, attempts are
// exhausted, or ctx is done. Between attempts it sleeps Backoff(p, attempt), or
// the classifier's retryAfter when that is larger. On a classifier-declined
// error it returns that error unwrapped; on exhaustion it wraps the last error
// with the attempt count.
func Do(ctx context.Context, p Policy, classify Classifier, fn func() error) error {
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt == attempts {
			break
		}
		ok, retryAfter := classify(err)
		if !ok {
			return err
		}
		delay := Backoff(p, attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled after %d attempt(s): %w", attempt, ctx.Err())
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("gave up after %d attempt(s): %w", attempts, err)
}

// Backoff returns the jittered delay to wait before the (attempt+1)-th try.
// attempt is 1-based. The pre-jitter delay is BaseDelay*2^(attempt-1) capped at
// MaxDelay; full jitter then draws a value in [0, that].
func Backoff(p Policy, attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	d := p.BaseDelay << (attempt - 1) // BaseDelay * 2^(attempt-1)
	if d <= 0 || d > p.MaxDelay {      // overflow, or over the cap
		d = p.MaxDelay
	}
	return jitter(d)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `env -u GOROOT go test ./internal/retry/ -v`
Expected: PASS (all six tests). Then `env -u GOROOT go vet ./internal/retry/` — no output.

- [ ] **Step 5: Commit**

```bash
git add internal/retry/
git commit -m "feat(retry): bounded exponential-backoff helper (Do + Backoff)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: `retry` + `claude` config keys

**Files:**
- Modify: `internal/config/config.go` (add `RetryConfig`, wire into `Config`, add `ClaudeConfig.MaxTransportRetries`)
- Test: `internal/config/config_test.go` (add cases)

**Interfaces:**
- Produces:
  - `type RetryConfig struct { MaxAttempts int; BaseDelay time.Duration; MaxDelay time.Duration }`
  - `Config.Retry RetryConfig` (`fig:"retry"`)
  - `ClaudeConfig.MaxTransportRetries int` (`fig:"max_transport_retries" default:"2"`)
  - Env overrides: `WAZIR_RETRY_MAX_ATTEMPTS`, `WAZIR_RETRY_BASE_DELAY`, `WAZIR_RETRY_MAX_DELAY`, `WAZIR_CLAUDE_MAX_TRANSPORT_RETRIES`.
- Consumes: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestRetryDefaults(t *testing.T) {
	t.Setenv("WAZIR_GITHUB_APP_ID", "1")
	t.Setenv("WAZIR_GITHUB_INSTALLATION_ID", "2")
	t.Setenv("WAZIR_GITHUB_PRIVATE_KEY", "dummy-pem")
	t.Setenv("WAZIR_PROJECT_OWNER", "acme")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Retry.MaxAttempts != 4 {
		t.Errorf("Retry.MaxAttempts = %d, want 4", c.Retry.MaxAttempts)
	}
	if c.Retry.BaseDelay != 500*time.Millisecond {
		t.Errorf("Retry.BaseDelay = %v, want 500ms", c.Retry.BaseDelay)
	}
	if c.Retry.MaxDelay != 8*time.Second {
		t.Errorf("Retry.MaxDelay = %v, want 8s", c.Retry.MaxDelay)
	}
	if c.Claude.MaxTransportRetries != 2 {
		t.Errorf("Claude.MaxTransportRetries = %d, want 2", c.Claude.MaxTransportRetries)
	}
}

func TestRetryEnvOverrides(t *testing.T) {
	t.Setenv("WAZIR_GITHUB_APP_ID", "1")
	t.Setenv("WAZIR_GITHUB_INSTALLATION_ID", "2")
	t.Setenv("WAZIR_GITHUB_PRIVATE_KEY", "dummy-pem")
	t.Setenv("WAZIR_PROJECT_OWNER", "acme")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")
	t.Setenv("WAZIR_RETRY_MAX_ATTEMPTS", "7")
	t.Setenv("WAZIR_RETRY_BASE_DELAY", "250ms")
	t.Setenv("WAZIR_CLAUDE_MAX_TRANSPORT_RETRIES", "3")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Retry.MaxAttempts != 7 || c.Retry.BaseDelay != 250*time.Millisecond {
		t.Errorf("overrides not applied: %+v", c.Retry)
	}
	if c.Claude.MaxTransportRetries != 3 {
		t.Errorf("Claude.MaxTransportRetries = %d, want 3", c.Claude.MaxTransportRetries)
	}
}
```

(If `config_test.go` lacks a `time` import, add it. The existing env-only `Load("")` cases in this file already show the required GitHub/Project env vars — mirror them.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `env -u GOROOT go test ./internal/config/ -run 'TestRetry' -v`
Expected: FAIL — `c.Retry` undefined and `c.Claude.MaxTransportRetries` undefined (compile error).

- [ ] **Step 3: Write the implementation**

In `internal/config/config.go`, add the `Retry` field to `Config`:

```go
type Config struct {
	GitHub   GitHubConfig  `fig:"github"`
	Project  ProjectConfig `fig:"project"`
	Repos    []string      `fig:"repos"`
	BotLogin string        `fig:"bot_login"`
	Store    StoreConfig   `fig:"store"`
	Claude   ClaudeConfig  `fig:"claude"`
	Forge    ForgeConfig   `fig:"forge"`
	Retry    RetryConfig   `fig:"retry"`
}
```

Add the new struct (place it near `ForgeConfig`):

```go
// RetryConfig tunes the bounded backoff applied to transient GitHub HTTP calls
// (REST + GraphQL + forge PRs). Reload-safe: read per-call via the transport's
// atomic policy holder (M5 hardening).
type RetryConfig struct {
	MaxAttempts int           `fig:"max_attempts" default:"4"`   // WAZIR_RETRY_MAX_ATTEMPTS
	BaseDelay   time.Duration `fig:"base_delay" default:"500ms"` // WAZIR_RETRY_BASE_DELAY
	MaxDelay    time.Duration `fig:"max_delay" default:"8s"`     // WAZIR_RETRY_MAX_DELAY
}
```

Add the claude field to `ClaudeConfig` (after `ReworkAllowedTools`):

```go
	MaxTransportRetries int `fig:"max_transport_retries" default:"2"` // WAZIR_CLAUDE_MAX_TRANSPORT_RETRIES — conservative claude-transport retry cap (restart-only)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `env -u GOROOT go test ./internal/config/ -v`
Expected: PASS (new + existing). Then `env -u GOROOT go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): retry section + claude.max_transport_retries

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Retrying HTTP transport in `githubauth`

**Files:**
- Create: `internal/githubauth/transport.go`
- Modify: `internal/githubauth/githubauth.go` (wrap the transport in `New`; add `Auth.SetRetryPolicy`)
- Test: `internal/githubauth/transport_test.go`

**Interfaces:**
- Consumes: `retry.Policy`, `retry.Backoff` (Task 1); `config.Config.Retry` (Task 2).
- Produces:
  - `func PolicyFromConfig(cfg config.Config) retry.Policy`
  - `Auth.SetRetryPolicy func(retry.Policy)` (new field on the existing `Auth` struct)
  - unexported: `type retryTransport struct{...}`, `func newRetryTransport(inner http.RoundTripper, p retry.Policy) *retryTransport`, `(*retryTransport).RoundTrip`, `(*retryTransport).setPolicy`, `func classifyHTTPResponse(*http.Response, error) (bool, time.Duration)`.

- [ ] **Step 1: Write the failing test**

`internal/githubauth/transport_test.go`:

```go
package githubauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EmadMokhtar/wazir/internal/retry"
)

// stubRT returns queued responses/errors in order.
type stubRT struct {
	steps []func(*http.Request) (*http.Response, error)
	calls int
	bodies []string // request body seen on each call
}

func (s *stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		s.bodies = append(s.bodies, string(b))
	} else {
		s.bodies = append(s.bodies, "")
	}
	step := s.steps[s.calls]
	s.calls++
	return step(req)
}

func resp(code int, hdr map[string]string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		h := http.Header{}
		for k, v := range hdr {
			h.Set(k, v)
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader("body"))}, nil
	}
}

func fastPolicy() retry.Policy {
	return retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
}

func TestRoundTripRetriesOn503(t *testing.T) {
	inner := &stubRT{steps: []func(*http.Request) (*http.Response, error){
		resp(503, nil), resp(503, nil), resp(200, nil),
	}}
	rt := newRetryTransport(inner, fastPolicy())
	r, err := rt.RoundTrip(newReq(t, "GET", nil))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if r.StatusCode != 200 || inner.calls != 3 {
		t.Fatalf("status=%d calls=%d, want 200/3", r.StatusCode, inner.calls)
	}
}

func TestRoundTripDoesNotRetry422(t *testing.T) {
	inner := &stubRT{steps: []func(*http.Request) (*http.Response, error){resp(422, nil)}}
	rt := newRetryTransport(inner, fastPolicy())
	r, err := rt.RoundTrip(newReq(t, "GET", nil))
	if err != nil || r.StatusCode != 422 || inner.calls != 1 {
		t.Fatalf("status=%d calls=%d err=%v, want 422/1/nil", r.StatusCode, inner.calls, err)
	}
}

func TestRoundTripExhaustsReturnsLastResponse(t *testing.T) {
	inner := &stubRT{steps: []func(*http.Request) (*http.Response, error){
		resp(503, nil), resp(503, nil), resp(503, nil),
	}}
	rt := newRetryTransport(inner, retry.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	r, err := rt.RoundTrip(newReq(t, "GET", nil))
	if err != nil || r == nil || r.StatusCode != 503 || inner.calls != 3 {
		t.Fatalf("status=%v calls=%d err=%v, want the final 503 after 3 tries", r, inner.calls, err)
	}
}

func TestRoundTripRewindsPostBody(t *testing.T) {
	inner := &stubRT{steps: []func(*http.Request) (*http.Response, error){resp(503, nil), resp(200, nil)}}
	rt := newRetryTransport(inner, fastPolicy())
	if _, err := rt.RoundTrip(newReq(t, "POST", []byte("payload"))); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if len(inner.bodies) != 2 || inner.bodies[0] != "payload" || inner.bodies[1] != "payload" {
		t.Fatalf("bodies=%v, want the body re-sent intact on retry", inner.bodies)
	}
}

func TestRoundTripRetriesTransportError(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Err: errTimeout{}}
	inner := &stubRT{steps: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return nil, netErr },
		resp(200, nil),
	}}
	rt := newRetryTransport(inner, fastPolicy())
	r, err := rt.RoundTrip(newReq(t, "GET", nil))
	if err != nil || r.StatusCode != 200 || inner.calls != 2 {
		t.Fatalf("status=%v calls=%d err=%v, want a retry then 200", r, inner.calls, err)
	}
}

func TestClassifyHTTPResponseRetryAfter(t *testing.T) {
	r := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"2"}}}
	ok, after := classifyHTTPResponse(r, nil)
	if !ok || after != 2*time.Second {
		t.Fatalf("ok=%v after=%v, want true/2s", ok, after)
	}
	if ok, _ := classifyHTTPResponse(&http.Response{StatusCode: 404}, nil); ok {
		t.Fatal("404 must not be retryable")
	}
	if ok, _ := classifyHTTPResponse(nil, errors.New("boom")); ok {
		t.Fatal("a non-net error must not be retryable")
	}
}

// errTimeout is a net.Error whose Timeout() is true.
type errTimeout struct{}

func (errTimeout) Error() string   { return "i/o timeout" }
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }

func newReq(t *testing.T, method string, body []byte) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	if body == nil {
		r, err = http.NewRequestWithContext(context.Background(), method, "http://example.test/x", nil)
	} else {
		r, err = http.NewRequestWithContext(context.Background(), method, "http://example.test/x", bytes.NewReader(body))
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `env -u GOROOT go test ./internal/githubauth/ -run RoundTrip -v`
Expected: FAIL — `undefined: newRetryTransport`, `undefined: classifyHTTPResponse`.

- [ ] **Step 3: Write the implementation**

`internal/githubauth/transport.go`:

```go
package githubauth

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/retry"
)

// PolicyFromConfig maps the retry config section to a retry.Policy.
func PolicyFromConfig(cfg config.Config) retry.Policy {
	return retry.Policy{
		MaxAttempts: cfg.Retry.MaxAttempts,
		BaseDelay:   cfg.Retry.BaseDelay,
		MaxDelay:    cfg.Retry.MaxDelay,
	}
}

// retryTransport wraps an inner RoundTripper with bounded backoff on transient
// HTTP responses (429 / 5xx, honoring Retry-After) and transport-level network
// errors. The policy is swappable at runtime (config reload).
type retryTransport struct {
	inner  http.RoundTripper
	policy atomic.Pointer[retry.Policy]
}

func newRetryTransport(inner http.RoundTripper, p retry.Policy) *retryTransport {
	rt := &retryTransport{inner: inner}
	rt.policy.Store(&p)
	return rt
}

func (rt *retryTransport) setPolicy(p retry.Policy) { rt.policy.Store(&p) }

// RoundTrip runs the request, retrying transient failures. It owns its own loop
// (rather than retry.Do) because it must drain/close intermediate response
// bodies and hand back the final *http.Response even on give-up, so go-github /
// githubv4 turn a persistent 5xx into their usual error.
func (rt *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p := *rt.policy.Load()
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var resp *http.Response
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 && req.Body != nil {
			// Rewind the body for the retry. net/http sets GetBody for the
			// in-memory bodies go-github/githubv4 send; without it we cannot
			// safely re-send, so stop and return what we last had.
			if req.GetBody == nil {
				return resp, err
			}
			body, gerr := req.GetBody()
			if gerr != nil {
				return resp, err
			}
			req.Body = body
		}
		resp, err = rt.inner.RoundTrip(req)
		ok, retryAfter := classifyHTTPResponse(resp, err)
		if !ok || attempt == attempts {
			return resp, err
		}
		if resp != nil { // drain so the connection can be reused, then discard
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		delay := retry.Backoff(p, attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
	}
	return resp, err
}

// classifyHTTPResponse decides whether (resp, err) from a RoundTrip is a
// transient failure worth retrying, and any minimum delay (a 429 Retry-After).
func classifyHTTPResponse(resp *http.Response, err error) (bool, time.Duration) {
	if err != nil {
		return isTransientNetErr(err), 0
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		return true, retryAfter(resp.Header)
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout: // 500/502/503/504
		return true, 0
	}
	return false, 0
}

func isTransientNetErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// Connection-level failures worth one more try.
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || isConnResetOrRefused(err)
}

func retryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}
```

Add a tiny platform helper `internal/githubauth/transport_syscall.go` (kept separate so the syscall import is isolated):

```go
package githubauth

import (
	"errors"
	"syscall"
)

func isConnResetOrRefused(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}
```

Then wire it into `New` in `internal/githubauth/githubauth.go`. Change the `Auth` struct and `New`:

```go
type Auth struct {
	HTTPClient     *http.Client                              // board REST+GraphQL AND forge REST (PRs)
	GitToken       func(ctx context.Context) (string, error) // a fresh installation token per git network op
	SetRetryPolicy func(retry.Policy)                        // hot-swap the HTTP retry policy (config reload)
}

func New(ctx context.Context, cfg config.Config) (Auth, error) {
	keyBytes, err := loadPrivateKey(cfg.GitHub.PrivateKey)
	if err != nil {
		return Auth{}, err
	}
	tr, err := ghinstallation.New(http.DefaultTransport, cfg.GitHub.AppID, cfg.GitHub.InstallationID, keyBytes)
	if err != nil {
		return Auth{}, fmt.Errorf("parse app private key: %w", err)
	}
	rt := newRetryTransport(tr, PolicyFromConfig(cfg)) // retries wrap the token-adding transport, so each try re-auths
	return Auth{
		HTTPClient:     &http.Client{Transport: rt},
		GitToken:       tr.Token,
		SetRetryPolicy: rt.setPolicy,
	}, nil
}
```

Add `"github.com/EmadMokhtar/wazir/internal/retry"` to the imports of `githubauth.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `env -u GOROOT go test ./internal/githubauth/ -v`
Expected: PASS (new transport tests + existing `TestNew*` / `TestLoadPrivateKey*`). Then `env -u GOROOT go vet ./internal/githubauth/`.

- [ ] **Step 5: Commit**

```bash
git add internal/githubauth/
git commit -m "feat(githubauth): retrying http.RoundTripper for transient GitHub HTTP

Wraps the shared ghinstallation transport, so board REST + GraphQL and
forge PR calls retry 429/5xx + transport errors with bounded backoff.
Fixes the GetCard-drop path for free. Policy is hot-swappable.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Git network-op retry in `forge/github`

**Files:**
- Modify: `internal/forge/github/git.go` (add `transientGit`; retry the network path in `gitRunner.run`; add `policy` field)
- Modify: `internal/forge/github/forge.go` (add `Options.RetryPolicy`; pass into `gitRunner`)
- Test: `internal/forge/github/git_test.go` (add cases)

**Interfaces:**
- Consumes: `retry.Policy`, `retry.Do` (Task 1).
- Produces:
  - `Options.RetryPolicy retry.Policy` (new field on the existing forge `Options`)
  - unexported: `gitRunner.policy retry.Policy`, `func transientGit(err error) bool`, `(gitRunner).runOnce(...)` (extracted current body).

- [ ] **Step 1: Write the failing test**

Append to `internal/forge/github/git_test.go`:

```go
// countingGit writes a fake `git` that fails transiently on its first N calls
// (printing a network-looking stderr, exit 1) then succeeds. It counts via a
// marker file so the count survives across processes.
func countingGit(t *testing.T, failFirst int) (bin string, countPath string) {
	t.Helper()
	dir := t.TempDir()
	countPath = filepath.Join(dir, "count")
	bin = filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"n=$(cat '" + countPath + "' 2>/dev/null || echo 0)\n" +
		"n=$((n+1)); echo $n > '" + countPath + "'\n" +
		"if [ \"$n\" -le " + fmt.Sprint(failFirst) + " ]; then\n" +
		"  echo \"fatal: unable to access 'https://x/': Could not resolve host: x\" >&2; exit 128\n" +
		"fi\n" +
		"echo ok\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, countPath
}

func TestTransientGitClassifier(t *testing.T) {
	yes := []string{
		"git push: exit 128 (stderr: fatal: unable to access 'https://x': Could not resolve host: x)",
		"stderr: Connection timed out",
		"stderr: the remote end hung up unexpectedly",
		"stderr: fatal: unable to access: The requested URL returned error: 503",
	}
	for _, s := range yes {
		if !transientGit(errors.New(s)) {
			t.Errorf("want transient: %q", s)
		}
	}
	no := []string{
		"stderr: CONFLICT (content): Merge conflict in a.go",
		"stderr: ! [rejected] main -> main (non-fast-forward)",
		"stderr: nothing to commit, working tree clean",
	}
	for _, s := range no {
		if transientGit(errors.New(s)) {
			t.Errorf("want NOT transient: %q", s)
		}
	}
	if transientGit(nil) {
		t.Error("nil must not be transient")
	}
}

func TestRunRetriesTransientNetworkOp(t *testing.T) {
	bin, _ := countingGit(t, 2) // fail twice, succeed on the 3rd
	old := retryTestJitter(t)   // pin jitter to 0 for a fast test (see helper below)
	defer old()
	g := gitRunner{
		bin:    bin,
		token:  func(context.Context) (string, error) { return "tok", nil },
		policy: retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	}
	out, err := g.run(context.Background(), "", true, "push", "origin", "b")
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v, want ok/nil after retries", out, err)
	}
}

func TestRunDoesNotRetryLocalOp(t *testing.T) {
	bin, countPath := countingGit(t, 5) // always fails within the test's attempts
	g := gitRunner{
		bin:    bin,
		token:  func(context.Context) (string, error) { return "tok", nil },
		policy: retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}
	if _, err := g.run(context.Background(), "", false, "worktree", "prune"); err == nil {
		t.Fatal("want an error; a local op must not retry")
	}
	if b, _ := os.ReadFile(countPath); strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("local op ran %s times, want exactly 1 (no retry)", strings.TrimSpace(string(b)))
	}
}
```

Add imports to `git_test.go` as needed: `"errors"`, `"os"`, `"path/filepath"`, `"time"`, and `"github.com/EmadMokhtar/wazir/internal/retry"`.

`retryTestJitter` cannot reach the `retry` package's unexported `jitter` from this package. Instead, keep the test fast purely via tiny `BaseDelay`/`MaxDelay` (≤5ms) — **delete the `retryTestJitter` lines** and the `old :=`/`defer old()` in `TestRunRetriesTransientNetworkOp`; the millisecond policy already makes real sleeps negligible.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `env -u GOROOT go test ./internal/forge/github/ -run 'TransientGit|RunRetries|RunDoesNotRetryLocal' -v`
Expected: FAIL — `undefined: transientGit`; `gitRunner` has no field `policy`.

- [ ] **Step 3: Write the implementation**

In `internal/forge/github/git.go`, add the `policy` field and split `run` into a retrying wrapper + `runOnce`, and add the classifier:

```go
type gitRunner struct {
	bin    string
	token  func(ctx context.Context) (string, error)
	policy retry.Policy // applied to network ops only
}

// run execs `git args...`. For network ops (auth == true) it retries transient
// failures (host resolution, timeouts, remote 5xx) with bounded backoff; local
// ops run exactly once.
func (g gitRunner) run(ctx context.Context, dir string, auth bool, args ...string) (string, error) {
	if !auth {
		return g.runOnce(ctx, dir, auth, args...)
	}
	var out string
	err := retry.Do(ctx, g.policy,
		func(err error) (bool, time.Duration) { return transientGit(err), 0 },
		func() error {
			var e error
			out, e = g.runOnce(ctx, dir, auth, args...)
			return e
		})
	return out, err
}

// runOnce is a single git invocation (the former body of run).
func (g gitRunner) runOnce(ctx context.Context, dir string, auth bool, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env := curatedGitEnv()
	if auth && g.token != nil {
		tok, err := g.token(ctx)
		if err != nil {
			return "", fmt.Errorf("get git token: %w", err)
		}
		env = append(env, authConfigEnv(tok)...)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// transientGit reports whether a git error looks like a retryable network
// failure (stderr matched case-insensitively). It deliberately excludes logic
// failures — merge conflicts, non-fast-forward pushes, auth, "nothing to commit".
func transientGit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, p := range []string{
		"could not resolve host",
		"connection timed out",
		"connection reset",
		"connection refused",
		"operation timed out",
		"early eof",
		"the remote end hung up",
		"rpc failed",
		"unable to access",  // git's curl wrapper for HTTP transport trouble
		"failed to connect",
		"gnutls_handshake",
		"ssl_",
		"tls",
		"error: 500", "error: 502", "error: 503", "error: 504",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
```

Add `"time"` and `"github.com/EmadMokhtar/wazir/internal/retry"` to `git.go` imports.

In `internal/forge/github/forge.go`, add the option and pass it through. In `Options` (line ~17) add:

```go
	RetryPolicy retry.Policy // bounded backoff for network git ops (clone/fetch/push)
```

In `New` (line ~49) change the `gitRunner` literal:

```go
		git:          gitRunner{bin: opts.GitBin, token: opts.GitToken, policy: opts.RetryPolicy},
```

Add `"github.com/EmadMokhtar/wazir/internal/retry"` to `forge.go` imports.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `env -u GOROOT go test ./internal/forge/github/ -v`
Expected: PASS (new + existing forge tests). Then `env -u GOROOT go vet ./internal/forge/github/`.

- [ ] **Step 5: Commit**

```bash
git add internal/forge/github/
git commit -m "feat(forge): retry transient network git ops (clone/fetch/push)

Bounded backoff at the single gitRunner.run network chokepoint; local
ops (worktree/prune) run once. transientGit excludes merge conflicts,
non-fast-forward, and auth failures.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Conservative `claude` transport retry

**Files:**
- Modify: `internal/claude/runner.go` (add `maxTransportRetries` field; retry loop around the exec in `Run`; `transientClaude`)
- Modify: `internal/claude/brain.go` (pass `cfg.MaxTransportRetries` into the `Runner` in `New`)
- Test: `internal/claude/runner_test.go` (add cases)

**Interfaces:**
- Consumes: `retry.Backoff` (Task 1); `config.ClaudeConfig.MaxTransportRetries` (Task 2).
- Produces:
  - `Runner.maxTransportRetries int` (set at construction; restart-only)
  - unexported: `func transientClaude(res RunResult, err error) bool`, `(*Runner).runOnce(...)` (extracted current body), package var `transportBaseDelay time.Duration` (overridable by tests).

- [ ] **Step 1: Write the failing test**

Append to `internal/claude/runner_test.go`:

```go
// countingClaude writes a fake `claude` that exits non-zero with a chosen stderr
// on its first failFirst calls, then prints a success envelope. Counts via a
// marker file.
func countingClaude(t *testing.T, failFirst int, failStderr, okText string) string {
	t.Helper()
	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"n=$(cat '" + countPath + "' 2>/dev/null || echo 0)\n" +
		"n=$((n+1)); echo $n > '" + countPath + "'\n" +
		"if [ \"$n\" -le " + fmt.Sprint(failFirst) + " ]; then\n" +
		"  echo '" + failStderr + "' >&2; exit 1\n" +
		"fi\n" +
		"cat <<'EOF'\n" + envelope(okText, false, "success") + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTransientClaudeClassifier(t *testing.T) {
	// No work happened (empty result) + a transport-ish error => retry.
	if !transientClaude(RunResult{}, errors.New("claude exec: exec: \"claude\": executable file not found in $PATH")) {
		t.Error("spawn failure must be transient")
	}
	if !transientClaude(RunResult{}, errors.New("claude exec: exit status 1 (stderr: overloaded_error 529)")) {
		t.Error("overloaded 529 with no work must be transient")
	}
	// Work happened / model-reported outcome => never retry.
	if transientClaude(RunResult{IsError: true, Subtype: "error_during_execution", SessionID: "s1"}, errors.New("claude reported failure")) {
		t.Error("a model-reported failure must NOT be transient")
	}
	if transientClaude(RunResult{Text: "partial"}, errors.New("claude exec: overloaded")) {
		t.Error("any produced result means work happened; must NOT retry")
	}
	// A timeout is not retried (work likely ran the full duration).
	if transientClaude(RunResult{}, errors.New("claude timed out after 5m")) {
		t.Error("timeout must NOT be transient")
	}
	if transientClaude(RunResult{}, nil) {
		t.Error("nil error must NOT be transient")
	}
}

func TestRunnerRetriesTransportFailure(t *testing.T) {
	oldDelay := transportBaseDelay
	transportBaseDelay = time.Millisecond
	defer func() { transportBaseDelay = oldDelay }()

	bin := countingClaude(t, 1, "overloaded_error 529", "recovered")
	r := &Runner{bin: bin, log: zap.NewNop(), maxTransportRetries: 2}
	res, err := r.Run(context.Background(), RunSpec{Prompt: "hi"})
	if err != nil || res.Text != "recovered" {
		t.Fatalf("res=%+v err=%v, want a retry then success", res, err)
	}
}

func TestRunnerDoesNotRetryModelFailure(t *testing.T) {
	// is_error=true is a real turn outcome; runOnce returns a populated result +
	// error, so Run must return immediately without a second invocation.
	bin := writeFakeClaude(t, envelope("boom", true, "error_during_execution"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop(), maxTransportRetries: 3}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "hi"}); err == nil {
		t.Fatal("want the model-reported failure surfaced")
	}
}
```

Add imports to `runner_test.go` if missing: `"errors"`, `"path/filepath"`, `"time"`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `env -u GOROOT go test ./internal/claude/ -run 'TransientClaude|RetriesTransport|DoesNotRetryModel' -v`
Expected: FAIL — `undefined: transientClaude`; `Runner` has no field `maxTransportRetries`; `undefined: transportBaseDelay`.

- [ ] **Step 3: Write the implementation**

In `internal/claude/runner.go`, add the field + a package var, split `Run` into a retry loop over `runOnce`, and add the classifier. Change the `Runner` struct:

```go
type Runner struct {
	bin                 string
	log                 *zap.Logger
	maxTransportRetries int // conservative pre-work transport retry cap (restart-only)
}

// transportBaseDelay is the base backoff between claude transport retries.
// A package var so tests can shrink it. Real default is set in Run.
var transportBaseDelay = 2 * time.Second
```

Rename the existing `Run` method body to `runOnce` (same signature, same body). Then add the new `Run` wrapper and classifier:

```go
// Run executes a headless claude invocation. It retries only conservative
// TRANSPORT failures that happened before any work (process spawn failure, an
// overload/529 with no session or result) up to maxTransportRetries; a
// model-reported failure (is_error / non-success subtype) is returned as-is.
func (r *Runner) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	attempts := r.maxTransportRetries
	if attempts < 1 {
		attempts = 1
	}
	policy := retry.Policy{MaxAttempts: attempts, BaseDelay: transportBaseDelay, MaxDelay: 10 * time.Second}
	var res RunResult
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		res, err = r.runOnce(ctx, spec)
		if err == nil || attempt == attempts || !transientClaude(res, err) {
			return res, err
		}
		r.log.Warn("claude transport failure; retrying", zap.Int("attempt", attempt), zap.Error(err))
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(retry.Backoff(policy, attempt)):
		}
	}
	return res, err
}

// transientClaude is deliberately conservative: it retries ONLY when no paid
// work happened (empty result envelope) AND the error looks like a spawn or
// pre-work overload/connection failure. Any produced result, is_error, or
// subtype means the turn ran — never retry (cost + partial side effects). A
// timeout is treated as work, not transport, so it is not retried.
func transientClaude(res RunResult, err error) bool {
	if err == nil {
		return false
	}
	if res.SessionID != "" || res.Text != "" || res.IsError || res.Subtype != "" {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "timed out") || strings.Contains(s, "cancelled") {
		return false
	}
	for _, p := range []string{
		"file not found", "exec format", "permission denied",
		"overloaded", "api_error", "529",
		"connection reset", "connection refused",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
```

Add `"github.com/EmadMokhtar/wazir/internal/retry"` to `runner.go` imports (`time` and `strings` are already imported).

In `internal/claude/brain.go` `New` (line ~128), pass the cap into the runner:

```go
	b := &ClaudeBrain{runner: &Runner{bin: cfg.Bin, log: log, maxTransportRetries: cfg.MaxTransportRetries}, log: log}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `env -u GOROOT go test ./internal/claude/ -v`
Expected: PASS (new + existing runner/brain tests). Then `env -u GOROOT go vet ./internal/claude/`.

- [ ] **Step 5: Commit**

```bash
git add internal/claude/
git commit -m "feat(claude): conservative transport retry for pre-work failures

Retry only spawn/overload/529 failures where no session or result was
produced; a model-reported failure or a timeout is never retried.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Wire the policy into `serve` (+ reload) and document

**Files:**
- Modify: `cmd/wazir/serve.go` (hot-swap the HTTP retry policy on reload)
- Modify: `wazir.example.yaml` (document the `retry` section + `claude.max_transport_retries`)
- Modify: `CLAUDE.md` (configuration + gotcha notes)
- Test: `cmd/wazir/serve_test.go` (assert `restartOnlyChanged` ignores `retry.*`)

**Interfaces:**
- Consumes: `githubauth.PolicyFromConfig` + `Auth.SetRetryPolicy` (Task 3). `New` already reads `cfg.Retry` so startup wiring needs no change; only reload does.

- [ ] **Step 1: Write the failing test**

Append to `cmd/wazir/serve_test.go` (this file already tests `restartOnlyChanged`; mirror its style):

```go
func TestRetryChangeIsNotRestartOnly(t *testing.T) {
	base := config.Config{}
	base.Retry = config.RetryConfig{MaxAttempts: 4, BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second}
	next := base
	next.Retry.MaxAttempts = 9 // a retry change must be hot-reloadable, not restart-only

	if d := restartOnlyChanged(base, next); d != "" {
		t.Fatalf("retry change flagged restart-only: %q", d)
	}
}
```

Add `"time"` and the config import if the test file lacks them (check the existing imports first).

- [ ] **Step 2: Run the test to verify it fails or passes-trivially**

Run: `env -u GOROOT go test ./cmd/wazir/ -run TestRetryChangeIsNotRestartOnly -v`
Expected: PASS immediately (the current `restartOnlyChanged` compares only `GitHub/Project/Store/Forge/Claude.Bin`, so it already ignores `Retry`). This test is a **guard** that a later edit doesn't accidentally add `Retry` to the restart-only set. If it FAILS, `restartOnlyChanged` is wrongly flagging retry — remove that comparison.

- [ ] **Step 3: Add the reload hot-swap**

In `cmd/wazir/serve.go`, inside the `config.Watch` reload callback (after the existing `worker.SetMaxBrainstormTurns(...)` line, ~line 149), add:

```go
					auth.SetRetryPolicy(githubauth.PolicyFromConfig(newCfg))
```

`auth` is already in scope in `runServe` and captured by the closure. No other startup change is needed — `githubauth.New(ctx, cfg)` already applied `PolicyFromConfig(cfg)` at construction (Task 3).

- [ ] **Step 4: Run the build + full suite**

Run: `env -u GOROOT go build ./... && env -u GOROOT go test ./... && env -u GOROOT go vet ./...`
Expected: build clean; ALL packages PASS (including `internal/orchestrator/imports_test.go` — the core still imports no provider); vet silent.

- [ ] **Step 5: Document the config**

In `wazir.example.yaml`, add a `retry` block and the claude key. Place `retry:` as a new top-level section and add `max_transport_retries` under the existing `claude:` section:

```yaml
# Bounded backoff for transient GitHub HTTP calls (REST + GraphQL + forge PRs).
# Hot-reloadable via `wazir serve`.
retry:
  max_attempts: 4        # total tries including the first (WAZIR_RETRY_MAX_ATTEMPTS)
  base_delay: 500ms      # first backoff, doubles each try (WAZIR_RETRY_BASE_DELAY)
  max_delay: 8s          # per-attempt cap (WAZIR_RETRY_MAX_DELAY)

claude:
  # ... existing keys ...
  max_transport_retries: 2   # conservative claude-transport retry cap; restart-only (WAZIR_CLAUDE_MAX_TRANSPORT_RETRIES)
```

(Match the file's existing indentation/comment style; only add these keys.)

In `CLAUDE.md`, under the **Configuration (fig)** section, add a sentence:

> The `retry` section (`max_attempts`/`base_delay`/`max_delay`, env `WAZIR_RETRY_*`) tunes bounded exponential backoff for transient GitHub HTTP (REST + GraphQL + forge PRs), applied by a retrying `http.RoundTripper` in `internal/githubauth`; it is part of the `serve` live-reload safe subset. `claude.max_transport_retries` (default 2, restart-only) caps conservative retries of pre-work `claude` transport failures. Transient git network ops (clone/fetch/push) retry at the `gitRunner.run` chokepoint. Deterministic model-reported failures are never retried.

And under the **Gotchas** section, add:

> - **Retries are transient-only (M5).** A card only drops to `Failed` after retries are exhausted; a momentary 429/5xx, git host-resolution blip, or `claude` spawn/overload no longer nukes it. Never retry a paid model turn that ran (`is_error`/non-success subtype) — cost + partial side-effects.

- [ ] **Step 6: Commit**

```bash
git add cmd/wazir/serve.go cmd/wazir/serve_test.go wazir.example.yaml CLAUDE.md
git commit -m "feat(serve,docs): hot-reload the HTTP retry policy; document retries

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Goal (transient self-healing; deterministic failures still Fail) → Tasks 3/4/5 (retry) + the `transient*` classifiers exclude model-reported failures. ✅
- `internal/retry` leaf helper → Task 1. ✅
- Transport RoundTripper covering REST + GraphQL + forge PRs → Task 3 (wraps the one shared client). ✅
- HTTP-status classifier (429/5xx + Retry-After + net errors) → Task 3 `classifyHTTPResponse`. ✅
- `GetCard`-drop bug fixed for free → Task 3 (GetCard flows through the retrying client). ✅
- git chokepoint retry gated on network ops → Task 4 (`auth == true`). ✅
- `transientGit` excludes conflicts/non-ff/auth → Task 4 test + impl. ✅
- Conservative claude transport retry (pre-work only) → Task 5 `transientClaude`. ✅
- Policy + config (`retry.*`, `claude.max_transport_retries`) → Task 2. ✅
- Reload-safe `retry.*` via atomic policy → Task 3 (`atomic.Pointer`) + Task 6 (`SetRetryPolicy` on reload). ✅
- Lock-TTL constraint (budget ≪ 5m) → policy defaults (≈7.5s worst case) in Task 2, documented. ✅
- Observability (per-retry log) → Task 5 warn; the transport is silent per-attempt by design (kept simple) — acceptable. ✅
- Testing strategy (transport 503×2→200, 422 no-retry, Retry-After, body rewind; git transient-then-success + local no-retry; claude spawn/overload vs model-failure) → Tasks 3/4/5. ✅
- Core unchanged / `imports_test.go` green → Task 6 full-suite run. ✅

**Placeholder scan:** No "TBD"/"handle errors"/"similar to". Every code step shows full code. The `retryTestJitter` reference in Task 4's first draft is explicitly corrected in the same step (delete those lines; rely on ms-delays). ✅

**Type consistency:** `retry.Policy{MaxAttempts,BaseDelay,MaxDelay}`, `retry.Do`, `retry.Backoff`, `retry.Classifier` used identically across Tasks 3/4/5. `Auth.SetRetryPolicy`/`PolicyFromConfig` produced in Task 3, consumed in Task 6. `gitRunner.policy`/`Options.RetryPolicy` consistent in Task 4. `Runner.maxTransportRetries`/`transportBaseDelay`/`transientClaude` consistent in Task 5. `Config.Retry`/`RetryConfig`/`ClaudeConfig.MaxTransportRetries` from Task 2 consumed downstream. ✅
