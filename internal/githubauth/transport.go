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
		resp, err = rt.inner.RoundTrip(req)
		ok, retryAfter := classifyHTTPResponse(resp, err)
		if !ok || attempt == attempts {
			return resp, err
		}
		// We intend to retry. Rewind the request body for the next attempt
		// BEFORE draining/closing this response, so that if the body can't be
		// rewound we hand the caller this response with its body still OPEN and
		// readable — not one we already closed. net/http sets GetBody for the
		// in-memory bodies go-github/githubv4 send; a nil GetBody means we
		// cannot safely re-send, so we stop and return this response.
		if req.Body != nil {
			if req.GetBody == nil {
				return resp, err
			}
			body, gerr := req.GetBody()
			if gerr != nil {
				return resp, err
			}
			req.Body = body
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
