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
	steps  []func(*http.Request) (*http.Response, error)
	calls  int
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
