---
name: ax-discovery2-client
description: Use this skill when the user wants to integrate the github.com/axgrid/discovery2-client library into a Go project — either to register the current service with a discovery2 server, to find/balance other services, or both. Triggers on phrases like "add ax-discovery client", "ax-discovery2-client", "axgrid discovery", "register service in discovery", "подключи discovery", "добавь discovery2 клиент", "зарегистрируй сервис в discovery", "найди сервис через discovery", "балансировка через discovery", "service discovery in Go", "publish to discovery", "подключи к ax-discovery", "use discovery2-client".
version: 2.0.0
---

# ax-discovery2-client integration skill

Wires **github.com/axgrid/discovery2-client** into a user's Go project.
The library does two things — pick the ones the user actually needs:

1. **Self-registration** — this app announces itself to discovery2 and (in
   heartbeat mode) keeps the registration alive. Or hands liveness over to
   the server which actively probes (HTTP / TCP).
2. **Resolution & balancing** — this app discovers and load-balances calls
   to other services via `*Resolver` (`RoundRobin` / `Random` / `Weighted`).

Most apps want both. This skill **must ask before scaffolding**, because
register-only and resolve-only paths produce different code.

---

## Step 0 — sanity gate (do these before any questions)

Don't ask if the answer is already in the project.

1. Check `go.mod` exists. If not, this skill doesn't apply — say so and stop.
2. If `go.mod`'s `go` directive is older than `1.21`, suggest
   `go mod edit -go=1.23` first; only proceed if the user agrees.
3. Look for the entry point. Likely candidates, in order:
   - `cmd/<app>/main.go`
   - `main.go` at the repo root
   - The first `package main` file you find
   If multiple plausible candidates exist, **ask which one**. Don't guess.
4. Read the entry-point file. Identify (silently — don't ask):
   - Where the HTTP server is started (port, listen address)
   - Whether there's already a `context.Context` / cancel / signal handler
   - Whether a config struct or env-var loader is in use
   These inform *how* you'll splice the integration, not *whether*.

---

## Step 1 — ask the user (always)

Use `AskUserQuestion` with the questions below, in this order. Skip a
question if its answer is already obvious from the project (e.g. there's
clearly only one upstream the app calls).

### Q1: What's needed?

```
question:    "What do you want to wire up?"
header:      "Direction"
options:
  - "Register self (Recommended)" — "This service announces itself to discovery2. Most common."
  - "Resolve / balance other services" — "This service calls upstream services discovered via discovery2."
  - "Both" — "Register self AND call other services."
multiSelect: false
```

### Q2: Liveness check (only if Q1 includes registration)

```
question:    "How should discovery2 decide if this service is alive?"
header:      "Liveness"
options:
  - "Heartbeat (Recommended)" — "Library auto-pings discovery every TTL/3 seconds. Works behind NAT, no inbound reachability needed."
  - "HTTP probe" — "Discovery actively GETs your /healthz (or specified URL) every 15s. Requires discovery to reach the app's address."
  - "TCP probe" — "Discovery TCP-connects to each interface's port. Best for non-HTTP services."
  - "External (CheckNone)" — "I'll manage status by hand via the REST API."
multiSelect: false
```

### Q3: Upstream services (only if Q1 includes resolve/balance)

Free-text or multiSelect with project-derived options. If the user has
mentioned services in chat, pre-suggest those. If unknown, ask:

```
question:    "Which upstream services will this app call?"
header:      "Upstream"
options:
  - "Just one — I'll specify the name" — "(Other) lets you type it"
  - "Several — comma-separated names" — "Same; the skill will create one resolver per service"
  - "I don't know yet — leave a TODO" — "Skill will scaffold a single placeholder resolver"
multiSelect: false
```

(For the actual names, accept user free-text via the "Other" path.)

### Q4: Discovery URL & token (only if not already in env)

If the project already reads `DISCOVERY_URL` / `DISCOVERY_TOKEN`, skip
this. Otherwise ask:

```
question:    "How should the discovery URL be configured?"
header:      "Config source"
options:
  - "Env vars DISCOVERY_URL / DISCOVERY_TOKEN (Recommended)" — "Standard 12-factor pattern. Skill adds env reads."
  - "Project's existing config struct" — "Skill adds fields to the existing config."
  - "Hard-code for now (dev/spike)" — "Skill writes a literal localhost:8500 — flag to fix before deploying."
multiSelect: false
```

---

## Step 2 — fetch the dependency

Always:

```bash
go get github.com/axgrid/discovery2-client@latest
```

Always import as:

```go
import discovery "github.com/axgrid/discovery2-client"
```

The package name is `discovery`. The `discovery` alias is conventional and
reads cleanly at call sites. Do **not** use `client` (was the old name).

---

## Step 3 — generate code per the answers

### A. Self-registration (Q1 = Register or Both)

Add this helper next to `main()`:

```go
import (
    "context"
    "log"
    "os"

    discovery "github.com/axgrid/discovery2-client"
)

func registerSelf(ctx context.Context) (*discovery.Registered, error) {
    d := discovery.New(
        envOr("DISCOVERY_URL", "http://localhost:8500"),
        discovery.WithToken(os.Getenv("DISCOVERY_TOKEN")),
    )
    return d.Register(ctx, discovery.Registration{
        Service: envOr("SERVICE_NAME", "<APP_NAME>"),
        Address: envOr("SERVICE_ADDR", "127.0.0.1"),
        Interfaces: []discovery.Interface{
            {Name: "WEB", Protocol: "http", Port: <PORT>, HealthURL: "/healthz"},
        },
        TTLSeconds: 30,
        // <CHECK_MODE_LINE>
    })
}

func envOr(k, def string) string {
    if v, ok := os.LookupEnv(k); ok {
        return v
    }
    return def
}
```

Replace `<CHECK_MODE_LINE>` per Q2:

| Q2 answer | Line to inject |
|---|---|
| Heartbeat | (nothing — default behavior; delete the placeholder) |
| HTTP probe | `CheckMode: discovery.CheckHTTP, CheckIntervalSec: 15,` |
| TCP probe | `CheckMode: discovery.CheckTCP, CheckIntervalSec: 15,` |
| External | `CheckMode: discovery.CheckNone,` |

In `main()`, immediately after the HTTP server is wired up:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

reg, err := registerSelf(ctx)
if err != nil {
    log.Fatalf("discovery: %v", err)
}
defer reg.Close()
```

If the project already has a `context.Context` and signal-driven shutdown,
**reuse them** — wire `reg.Close()` into the existing shutdown path so
deregistration is synchronous on SIGTERM (don't just rely on `defer`,
which doesn't run if the process is killed).

**Tailoring rules:**

- `<PORT>` is the actual port the app listens on. Read it from existing
  config / flags rather than hard-coding.
- If the app exposes more than one port (HTTP + gRPC), add one `Interface`
  per port. Common interface names: `WEB`, `WS`, `GRPC`, `TCP`, `ADMIN` —
  short, uppercase.
- If there's no `/healthz` endpoint and Q2 = HTTP probe, add a minimal one
  (`w.WriteHeader(200)`).
- If the app is fronted by a reverse proxy with TLS termination
  (e.g. `https://my-svc.example.com`), set the interface to
  `Protocol: "https", Port: 443` (or `Port: 0`); the probe omits default
  ports cleanly. `HealthURL` may be a full URL pointing at a different host
  — use that when the public-facing hostname differs from the internal one.
- Don't invent a `Weight` value. If the user mentions canary / capacity
  shaping, surface it via a `SERVICE_WEIGHT` env var.

### B. Resolution & balancing (Q1 = Resolve or Both)

For each upstream from Q3 / chat context, create one `*Resolver` at startup,
reuse it for the lifetime of the app, `Close()` on shutdown.

```go
type upstreams struct {
    billing *discovery.Resolver
    // ... one field per upstream
}

func newUpstreams(ctx context.Context, d *discovery.Client) (*upstreams, error) {
    billing, err := d.NewResolver(ctx, "billing", discovery.RoundRobin)
    if err != nil { return nil, err }
    return &upstreams{billing: billing}, nil
}

func (u *upstreams) Close() {
    u.billing.Close()
}
```

Strategy default is `RoundRobin`. Pick `Weighted` only if the user
explicitly mentions weights, canary, or capacity shaping.

**Calling pattern.** Resolve at every request, don't cache the address:

```go
// HTTP:
base, err := u.billing.PickURL("WEB")    // "http://10.0.0.5:8080/"
// gRPC:
addr, err := u.billing.PickAddress("GRPC") // "10.0.0.5:9000"
```

For gRPC, just `grpc.Dial` to the picked address; re-pick on connection
errors. Don't write a custom gRPC `resolver.Builder` unless the user asks
— it's substantial code and not needed for most apps.

### C. Watch (rare)

Only if the user explicitly wants to *react* to topology changes (refresh a
cache, update a UI):

```go
ch, err := d.Watch(ctx)
if err != nil { /* … */ }
for ev := range ch {
    log.Printf("discovery: %s %s/%s", ev.Type, ev.Service, ev.Instance)
}
```

The channel closes on disconnect; reconnect in a backoff loop for
long-lived watches. For pure load-balancing the `*Resolver` already does
this internally — don't add `Watch` unless there's a genuine subscriber
need.

---

## Step 4 — env vars

Suggested names (only override if the project already has a different
convention):

| Env | Default | Meaning |
|---|---|---|
| `DISCOVERY_URL` | `http://localhost:8500` | One URL or comma-separated list of discovery2 nodes |
| `DISCOVERY_TOKEN` | (empty) | Bearer token; required if the server has write tokens configured |
| `SERVICE_NAME` | repo / cmd name | logical service this app registers as |
| `SERVICE_ADDR` | `127.0.0.1` | host or IP peers will use to reach this instance |
| `SERVICE_WEIGHT` | `1` | optional, for weighted balancing |

If the project uses viper / koanf / a custom config struct, add fields
there instead of reading raw env vars.

If the project has `.env.example`, also append the new variables there
with a one-line comment per variable.

---

## Step 5 — verify

After wiring:

1. `go build ./...` must succeed.
2. If a discovery2 server is reachable, suggest: run the app and
   `curl http://<discovery>/v1/services` to confirm the registration
   appeared. Don't run it yourself unless the user explicitly asks.
3. Show a one-line summary: which files changed, which env vars are needed.

---

## Common edge cases

- **App already uses Consul / etcd / DNS-SRV.** Don't replace it silently.
  Ask whether to migrate, run alongside, or abort.
- **App is a cron / one-shot job.** Probably shouldn't register. Ask.
- **Monorepo with multiple binaries.** Ask which binary; offer to do all.
- **Tests.** Don't touch test packages. Don't mock the client in unit
  tests; if the user wants tests against discovery, suggest httptest with
  the REST API stubbed.
- **Healthz missing AND user picked HTTP probe.** Add a minimal
  `/healthz` (`w.WriteHeader(200)`); do not invent business-logic
  readiness checks — that's the user's call.
- **Reverse-proxy-fronted services.** Use `Port: 0` (or 443) and
  `Protocol: "https"`; the probe URL omits default ports. `HealthURL`
  can be a full URL.

---

## What this skill does NOT do

- Doesn't run or install the discovery2 server. That's a separate concern;
  point at https://github.com/axgrid/discovery2 if asked.
- Doesn't generate fake addresses / ports — always pull from existing app
  config.
- Doesn't add metrics / tracing wrappers around the client; the user can
  layer those via `WithHTTPClient(...)`.
- Doesn't change auth flow in the user's app. Discovery's tokens are
  separate.

---

## Output style

- If the user writes in Russian, reply in Russian.
- One short paragraph per file changed when reporting back.
- End with: which env vars to set, and what the app does at startup now.
- Don't restate documentation the user can read in the linked README.
