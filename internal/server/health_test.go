package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthOK(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(m, "/healthz", nil)
		rec := httptest.NewRecorder()
		Health(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s /healthz = %d, want 200", m, rec.Code)
		}
	}
}

func TestHealthGETBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	Health(rec, req)
	if got := rec.Body.String(); got != "ok\n" {
		t.Errorf("GET /healthz body = %q, want %q", got, "ok\n")
	}
}

func TestHealthRejectsNonGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	Health(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
}
