// Package web wraps the engine as HTTP handlers for Vercel.
package web

import (
	"encoding/json"
	"net/http"
	"net/url"

	"gotest/internal/engine"
)

// Caps keep the hosted API small and within serverless time limits.
const (
	maxCases       = 20
	maxConcurrency = 50
	maxDurationMs  = 8000
	maxRate        = 500
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func validTarget(t string) bool {
	u, err := url.Parse(t)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func Run(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST a JSON suite")
		return
	}
	var s engine.Suite
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&s); err != nil {
		fail(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !validTarget(s.Target) || len(s.Tests) == 0 || len(s.Tests) > maxCases {
		fail(w, 400, "need a valid http(s) target and 1-20 tests")
		return
	}
	writeJSON(w, 200, engine.RunSuite(r.Context(), s, engine.NewClient(1, true)))
}

func Load(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "POST JSON load options")
		return
	}
	var o engine.LoadOpts
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&o); err != nil {
		fail(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !validTarget(o.Target) {
		fail(w, 400, "invalid target")
		return
	}
	o.Concurrency = clamp(o.Concurrency, 1, maxConcurrency)
	o.DurationMs = clamp(o.DurationMs, 500, maxDurationMs)
	o.Rate = clamp(o.Rate, 0, maxRate)
	writeJSON(w, 200, engine.Load(r.Context(), o, engine.NewClient(o.Concurrency, true)))
}
