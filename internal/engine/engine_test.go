package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func srv() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"abc","user":{"id":42}}`))
	})
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer abc" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"user":{"id":42}}`))
	})
	return httptest.NewServer(mux)
}

func ptr[T any](v T) *T { return &v }

func TestSuiteChaining(t *testing.T) {
	s := srv()
	defer s.Close()
	suite := Suite{Target: s.URL, Tests: []TestCase{
		{Name: "login", Request: Request{Method: "POST", Path: "/login"},
			Assert:  []Assertion{{Status: ptr(200)}, {JSON: "token", Exists: ptr(true)}},
			Capture: map[string]string{"token": "json:token"}},
		{Name: "me", Request: Request{Path: "/me", Headers: map[string]string{"Authorization": "Bearer {{token}}"}},
			Assert: []Assertion{{Status: ptr(200)}, {JSON: "user.id", Eq: 42.0}}},
		{Name: "bad", Request: Request{Path: "/me"}, Assert: []Assertion{{Status: ptr(200)}}},
	}}
	r := RunSuite(context.Background(), suite, NewClient(1, false))
	if r.Passed != 2 || r.Failed != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestLoadClosedAndOpen(t *testing.T) {
	s := srv()
	defer s.Close()
	for _, rate := range []int{0, 200} {
		r := Load(context.Background(), LoadOpts{Target: s.URL, Path: "/login", Concurrency: 8, DurationMs: 500, Rate: rate}, NewClient(8, false))
		if r.Requests == 0 || r.Errors != 0 || r.P99 < r.P50 {
			t.Fatalf("rate %d: %+v", rate, r)
		}
	}
}

// Server stalls 1s once; open-loop latency must reflect the stall.
func TestCoordinatedOmission(t *testing.T) {
	var first atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first.CompareAndSwap(false, true) {
			time.Sleep(time.Second)
		}
	}))
	defer s.Close()
	r := Load(context.Background(), LoadOpts{Target: s.URL, Path: "/", Concurrency: 1, DurationMs: 2000, Rate: 50}, NewClient(1, false))
	if r.Max < 900 || r.P90 < 100 {
		t.Fatalf("stall not reflected: %+v", r)
	}
}

func TestSSRFBlocked(t *testing.T) {
	s := srv()
	defer s.Close()
	r := RunSuite(context.Background(), Suite{Target: s.URL, Tests: []TestCase{{Name: "x", Request: Request{Path: "/login"}}}}, NewClient(1, true))
	if r.Failed != 1 {
		t.Fatal("loopback should be blocked")
	}
}
