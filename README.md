# GoTest

Concurrent API testing and load-testing tool in Go (stdlib only, zero dependencies).
One engine, two front-ends: a CLI and a Vercel-hosted web UI/API.

## Layout
- `internal/engine` – assertions, suite runner, scheduler/worker-pool/aggregator load generator
- `cmd/gotest` – CLI
- `api/run.go`, `api/load.go` – Vercel serverless functions (`POST /api/run`, `POST /api/load`)
- `public/index.html` – web UI

## CLI
    go build -o gotest ./cmd/gotest
    ./gotest run  -f suite.example.json            # exit code 1 on any failed assertion
    ./gotest load -u https://example.com -c 50 -d 10s
    ./gotest load -u https://example.com -c 50 -d 10s -r 500   # open-loop, 500 req/s
    go test -race ./...

## Deploy to Vercel
    npm i -g vercel
    vercel            # preview
    vercel --prod
Or push to GitHub and import the repo at vercel.com/new (no build settings needed).

## Suite format (JSON)
Assertions: `status`, `json`+`exists`/`eq`, `header`+`contains`, `body_contains`, `latency_lt_ms`.
Capture: `"capture": {"token": "json:token"}`, then use `{{token}}` in later paths/headers/bodies.

## Design notes
- Shutdown order: scheduler closes `jobs` -> workers exit -> `WaitGroup` -> close `results` -> aggregator finishes.
- Open-loop mode measures latency from the *intended* send time (avoids coordinated omission).
- Hosted API blocks private/loopback IPs at dial time (SSRF guard) and caps workers, duration and rate.

## Not yet implemented (from the proposal)
YAML suites, HDR histogram (percentiles currently come from a sorted slice), per-phase httptrace timings,
CSV/JUnit reporters, ramp-up, live terminal view.
