// Package engine holds GoTest's core: assertions, suite runner and load generator.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- Types ----------

type Assertion struct {
	Status       *int   `json:"status,omitempty"`
	Header       string `json:"header,omitempty"`
	JSON         string `json:"json,omitempty"`
	Exists       *bool  `json:"exists,omitempty"`
	Eq           any    `json:"eq,omitempty"`
	Contains     string `json:"contains,omitempty"`
	BodyContains string `json:"body_contains,omitempty"`
	LatencyLtMs  *int   `json:"latency_lt_ms,omitempty"`
}

type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type TestCase struct {
	Name    string            `json:"name"`
	Request Request           `json:"request"`
	Assert  []Assertion       `json:"assert"`
	Capture map[string]string `json:"capture,omitempty"` // var -> "json:path"
}

type Suite struct {
	Target string     `json:"target"`
	Tests  []TestCase `json:"tests"`
}

type CaseResult struct {
	Name      string   `json:"name"`
	Status    int      `json:"status"`
	LatencyMs float64  `json:"latency_ms"`
	Failures  []string `json:"failures,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type SuiteResult struct {
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	Cases  []CaseResult `json:"cases"`
}

// ---------- HTTP client ----------

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// NewClient returns a client whose transport is sized to the worker count so
// connections are reused. blockPrivate stops requests to internal addresses
// (SSRF guard for the hosted API); it checks the resolved IP at dial time.
func NewClient(workers int, blockPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second}
	if blockPrivate {
		d.Control = func(_, addr string, _ syscall.RawConn) error {
			host, _, _ := net.SplitHostPort(addr)
			if ip := net.ParseIP(host); ip != nil && isPrivate(ip) {
				return errors.New("blocked: private address")
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:         d.DialContext,
			MaxIdleConns:        workers,
			MaxIdleConnsPerHost: workers,
			IdleConnTimeout:     30 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}

func subst(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func do(ctx context.Context, c *http.Client, target string, r Request, vars map[string]string) (int, http.Header, []byte, error) {
	m := r.Method
	if m == "" {
		m = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, m, strings.TrimRight(target, "/")+subst(r.Path, vars),
		strings.NewReader(subst(r.Body, vars)))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, subst(v, vars))
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close() // always read + close so the connection is reused
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, body, nil
}

// ---------- Assertions ----------

func jsonPath(body []byte, path string) (any, bool) {
	var cur any
	if json.Unmarshal(body, &cur) != nil {
		return nil, false
	}
	for _, p := range strings.Split(path, ".") {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[p]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Check returns "" on success or a failure description.
func (a Assertion) Check(status int, h http.Header, body []byte, lat time.Duration) string {
	switch {
	case a.Status != nil:
		if status != *a.Status {
			return fmt.Sprintf("status: want %d, got %d", *a.Status, status)
		}
	case a.JSON != "":
		v, ok := jsonPath(body, a.JSON)
		if a.Exists != nil && ok != *a.Exists {
			return fmt.Sprintf("json %q: exists=%v, want %v", a.JSON, ok, *a.Exists)
		}
		if a.Eq != nil && (!ok || fmt.Sprint(v) != fmt.Sprint(a.Eq)) {
			return fmt.Sprintf("json %q: want %v, got %v", a.JSON, a.Eq, v)
		}
	case a.Header != "":
		v := h.Get(a.Header)
		if a.Contains != "" && !strings.Contains(v, a.Contains) {
			return fmt.Sprintf("header %s: %q does not contain %q", a.Header, v, a.Contains)
		}
		if a.Contains == "" && v == "" {
			return fmt.Sprintf("header %s missing", a.Header)
		}
	case a.BodyContains != "":
		if !bytes.Contains(body, []byte(a.BodyContains)) {
			return fmt.Sprintf("body does not contain %q", a.BodyContains)
		}
	case a.LatencyLtMs != nil:
		if lat >= time.Duration(*a.LatencyLtMs)*time.Millisecond {
			return fmt.Sprintf("latency %v >= %dms", lat.Round(time.Millisecond), *a.LatencyLtMs)
		}
	}
	return ""
}

// ---------- Functional runner ----------

// RunSuite runs cases in order, chaining captured variables between them.
func RunSuite(ctx context.Context, s Suite, c *http.Client) SuiteResult {
	vars := map[string]string{}
	res := SuiteResult{}
	for _, tc := range s.Tests {
		cr := CaseResult{Name: tc.Name}
		start := time.Now()
		status, h, body, err := do(ctx, c, s.Target, tc.Request, vars)
		lat := time.Since(start)
		cr.Status, cr.LatencyMs = status, float64(lat.Microseconds())/1000
		if err != nil {
			cr.Error = err.Error()
		} else {
			for _, a := range tc.Assert { // record all failures, not just the first
				if f := a.Check(status, h, body, lat); f != "" {
					cr.Failures = append(cr.Failures, f)
				}
			}
			for name, expr := range tc.Capture {
				if p, ok := strings.CutPrefix(expr, "json:"); ok {
					if v, found := jsonPath(body, p); found {
						vars[name] = fmt.Sprint(v)
					}
				}
			}
		}
		if cr.Error != "" || len(cr.Failures) > 0 {
			res.Failed++
		} else {
			res.Passed++
		}
		res.Cases = append(res.Cases, cr)
		if ctx.Err() != nil {
			break
		}
	}
	return res
}

// ---------- Load generator ----------

type LoadOpts struct {
	Target      string            `json:"target"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	Concurrency int               `json:"concurrency"`
	DurationMs  int               `json:"duration_ms"`
	Rate        int               `json:"rate"` // req/s; 0 = closed loop
}

type LoadReport struct {
	Mode     string        `json:"mode"`
	Requests int64         `json:"requests"`
	Errors   int64         `json:"errors"`
	Status   map[int]int64 `json:"status"`
	RPS      float64       `json:"rps"`
	P50      float64       `json:"p50_ms"`
	P90      float64       `json:"p90_ms"`
	P95      float64       `json:"p95_ms"`
	P99      float64       `json:"p99_ms"`
	Max      float64       `json:"max_ms"`
}

type sample struct {
	lat    time.Duration
	status int
	err    bool
}

// Load runs scheduler -> worker pool -> aggregator. In rate mode latency is
// measured from each request's intended send time (coordinated-omission safe).
func Load(parent context.Context, o LoadOpts, c *http.Client) LoadReport {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	runCtx, cancel := context.WithTimeout(parent, time.Duration(o.DurationMs)*time.Millisecond)
	defer cancel()

	jobs := make(chan time.Time, o.Concurrency*2)
	results := make(chan sample, o.Concurrency*2)
	req := Request{Method: o.Method, Path: o.Path, Headers: o.Headers, Body: o.Body}
	begin := time.Now()

	// Scheduler: the only goroutine that closes jobs.
	go func() {
		defer close(jobs)
		if o.Rate <= 0 {
			for runCtx.Err() == nil {
				select {
				case jobs <- time.Now():
				case <-runCtx.Done():
					return
				}
			}
			return
		}
		interval := time.Second / time.Duration(o.Rate)
		next := begin
		for {
			if d := time.Until(next); d > 0 {
				select {
				case <-time.After(d):
				case <-runCtx.Done():
					return
				}
			}
			select {
			case jobs <- next: // blocks when saturated; Intended stays in the past
			case <-runCtx.Done():
				return
			}
			next = next.Add(interval)
		}
	}()

	// Workers use the parent ctx so in-flight requests drain after the deadline.
	var wg sync.WaitGroup
	for i := 0; i < o.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for intended := range jobs {
				status, _, _, err := do(parent, c, o.Target, req, nil)
				results <- sample{time.Since(intended), status, err != nil || status >= 500}
			}
		}()
	}
	// Close results only after every worker has exited (no send-on-closed panic).
	go func() { wg.Wait(); close(results) }()

	rep := LoadReport{Mode: "closed-loop", Status: map[int]int64{}}
	if o.Rate > 0 {
		rep.Mode = fmt.Sprintf("open-loop %d req/s", o.Rate)
	}
	var lats []time.Duration
	for s := range results { // single writer: no locks needed
		rep.Requests++
		if s.err {
			rep.Errors++
		}
		if s.status > 0 {
			rep.Status[s.status]++
		}
		lats = append(lats, s.lat)
	}
	rep.RPS = float64(rep.Requests) / time.Since(begin).Seconds()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		i := int(p*float64(len(lats))+0.999999) - 1
		return float64(lats[max(i, 0)].Microseconds()) / 1000
	}
	rep.P50, rep.P90, rep.P95, rep.P99, rep.Max = pct(.5), pct(.9), pct(.95), pct(.99), pct(1)
	return rep
}
