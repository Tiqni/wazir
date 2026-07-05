package server

import "net/http"

// Health is a liveness endpoint for the webhook receiver. It answers 200 to
// GET/HEAD so uptime checks (and manual `curl https://.../healthz` probes) can
// confirm `wazir serve` is reachable through the tunnel without POSTing a signed
// webhook payload. It performs no store or board I/O on purpose: the daemon fails
// startup if the store can't open or the board isn't hydrated, so a server that is
// listening is already ready to receive events.
func Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("ok\n"))
	}
}
