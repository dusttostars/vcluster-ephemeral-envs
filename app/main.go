package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

var startTime = time.Now()

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		uptime := time.Since(startTime).Round(time.Second)

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
  <title>Ephemeral App</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body { font-family: -apple-system, sans-serif; background: #0a0a0a; color: #fff; display: flex; align-items: center; justify-content: center; min-height: 100vh; }
    .card { background: #1a1a1a; border-radius: 16px; padding: 40px; max-width: 500px; width: 90%%; }
    h1 { font-size: 28px; margin-bottom: 8px; }
    .subtitle { color: #a855f7; font-size: 14px; font-family: monospace; margin-bottom: 24px; }
    .row { display: flex; justify-content: space-between; padding: 12px 0; border-bottom: 1px solid rgba(255,255,255,0.06); }
    .row:last-child { border-bottom: none; }
    .label { color: #71717a; font-size: 13px; }
    .value { font-family: monospace; font-size: 13px; color: #22c55e; }
    .pulse { display: inline-block; width: 8px; height: 8px; background: #22c55e; border-radius: 50%%; margin-right: 8px; animation: pulse 2s infinite; }
    @keyframes pulse { 0%%,100%% { opacity: 1; } 50%% { opacity: 0.3; } }
  </style>
</head>
<body>
  <div class="card">
    <h1><span class="pulse"></span>Ephemeral App</h1>
    <p class="subtitle">Running inside a vCluster</p>
    <div class="row"><span class="label">Hostname</span><span class="value">%s</span></div>
    <div class="row"><span class="label">Branch</span><span class="value">%s</span></div>
    <div class="row"><span class="label">Commit</span><span class="value">%s</span></div>
    <div class="row"><span class="label">Uptime</span><span class="value">%s</span></div>
    <div class="row"><span class="label">Port</span><span class="value">%s</span></div>
  </div>
</body>
</html>`,
			hostname,
			envOr("GIT_BRANCH", "unknown"),
			envOr("GIT_COMMIT", "unknown"),
			uptime,
			port,
		)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	log.Printf("ephemeral app listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
