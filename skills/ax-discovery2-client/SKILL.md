---
name: ax-discovery2-client
description: Use this skill when the user wants to integrate the github.com/axgrid/ax-discovery2-client library into a Go project — to register the current service with a discovery2 server (with a semver version), find/balance other services (incl. version-constrained and sticky/token balancing), and/or read & write the discovery2 config store (typed variables, scoped global/service/version). Triggers on phrases like "add ax-discovery client", "ax-discovery2-client", "axgrid discovery", "register service in discovery", "подключи discovery", "добавь discovery2 клиент", "зарегистрируй сервис в discovery", "найди сервис через discovery", "балансировка через discovery", "service discovery in Go", "publish to discovery", "подключи к ax-discovery", "use discovery2-client", "read config from discovery", "discovery config variables", "конфиг из discovery", "переменные из discovery", "версия сервиса в discovery", "sticky balancing discovery", "discover by version".
version: 2.1.0
---

# ax-discovery2-client integration skill

> **Go import path:** `github.com/axgrid/ax-discovery2-client` (per the library's
> `go.mod` — use this in `import` and `go get`).
> **Source repository:** <https://github.com/AxGrid/ax-discovery2-client>
> (Owner casing differs from the import path — GitHub resolves it; always import
> the lowercase path above.)

Wires **github.com/axgrid/ax-discovery2-client** into a user's Go project.
The library does three things — pick the ones the user actually needs:

1. **Self-registration** — this app announces itself to discovery2 (with a
   semver **version**) and, in heartbeat mode, keeps the registration alive.
   Or hands liveness over to the server which actively probes (HTTP / TCP).
2. **Resolution & balancing** — discovers and load-balances calls to other
   services via `*Resolver` (`RoundRobin` / `Random` / `Weighted`), optionally
   **version-constrained** (`WithVersion(">=2.1.0")`). For session affinity,
   server-side `d.Pick(...)` supports **sticky tokens**.
3. **Config store** — reads its effective configuration (typed variables,
   merged `global < service < version`) and/or writes config blocks.

Cross-cutting, cheap, always-on in the scaffold:

- **Version** on registration (`Registration.Version`) so others can filter by it.
- **Client name** (`discovery.WithName(...)`) so this app shows up by name in the
  server dashboard's request feed and client map (falls back to IP otherwise).

Most apps want registration + one of the others. This skill **must ask before
scaffolding**, because the paths produce different code.

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

### Q1b: Config store

```
question:    "Does this app read (or write) its configuration from discovery2's config store?"
header:      "Config"
options:
  - "No (Recommended)" — "Skip config wiring entirely."
  - "Read config" — "App pulls its effective config (global < service < version) at startup and/or on changes."
  - "Read + write" — "App also publishes config blocks (needs write access to the scope; global → admin)."
multiSelect: false
```

Most apps that register also read config eventually, but don't push it on them
— only scaffold config code if they say yes here. Writing config from an app is
rarer (usually an operator/CLI job); confirm before scaffolding writes.

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
go get github.com/axgrid/ax-discovery2-client@latest
```

Always import as:

```go
import discovery "github.com/axgrid/ax-discovery2-client"
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

    discovery "github.com/axgrid/ax-discovery2-client"
)

func registerSelf(ctx context.Context) (*discovery.Client, *discovery.Registered, error) {
    name := envOr("SERVICE_NAME", "<APP_NAME>")
    d := discovery.New(
        envOr("DISCOVERY_URL", "http://localhost:8500"),
        discovery.WithToken(os.Getenv("DISCOVERY_TOKEN")),
        discovery.WithName(name), // shows up by name in the dashboard's request feed
    )
    reg, err := d.Register(ctx, discovery.Registration{
        Service: name,
        Address: envOr("SERVICE_ADDR", "127.0.0.1"),
        Version: envOr("SERVICE_VERSION", "<VERSION>"), // semver — lets callers filter by version
        Interfaces: []discovery.Interface{
            {Name: "WEB", Protocol: "http", Port: <PORT>, HealthURL: "/healthz"},
        },
        TTLSeconds: 30,
        // <CHECK_MODE_LINE>
    })
    return d, reg, err
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

d, reg, err := registerSelf(ctx)
if err != nil {
    log.Fatalf("discovery: %v", err)
}
defer reg.Close()
_ = d // reuse this *discovery.Client for resolvers and config (below)
```

If the app only resolves/reads config and does **not** register, build the
client directly instead — still pass `WithName`:

```go
d := discovery.New(
    envOr("DISCOVERY_URL", "http://localhost:8500"),
    discovery.WithToken(os.Getenv("DISCOVERY_TOKEN")),
    discovery.WithName(envOr("SERVICE_NAME", "<APP_NAME>")),
)
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
- `Version` should be a real semver. Prefer a build-time value injected via
  `-ldflags "-X main.version=2.1.0"` (read it in `registerSelf`), falling back
  to `SERVICE_VERSION`. If the project has no version convention, default the
  env to the app's current tag/`0.1.0` and leave a TODO — don't fabricate.
- `WithName` defaults to the service name; only override if the app runs many
  distinct callers that should be told apart (e.g. `billing-worker` vs
  `billing-cron`).

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

**Version-constrained resolution.** If the app must only talk to certain
versions of an upstream (e.g. during a migration), pass `WithVersion` — the
filter is re-applied on every refresh, so rolling deploys are reflected live:

```go
billing, err := d.NewResolver(ctx, "billing", discovery.RoundRobin,
    discovery.WithVersion(">=2.1.0")) // npm-style: ^2.1, ~2.1.0, 1.x, ranges
```

Instances with an empty / non-semver version are excluded under a constraint.

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

### D. Server-side pick / sticky sessions (only if asked)

A long-lived `*Resolver` is the default. Reach for server-side `d.Pick` when
the app wants **session affinity** — the same token keeps hitting the same
instance until it idles out (the binding is persisted + cluster-replicated, and
re-binds if that instance dies). Good for sticky user sessions, stateful upstreams.

```go
pick, err := d.Pick(ctx, "billing", discovery.PickOptions{
    Token:   sessionID,    // same token → same instance
    Version: ">=2.1.0",    // optional semver filter
    Iface:   "WEB",        // which interface to resolve into address/url
})
// pick.Address ("10.0.0.5:8080"), pick.URL, pick.Instance, pick.Rebound
```

Without a `Token` it's a stateless weighted pick (one call, no resolver kept).
Use the resolver for high-RPS fan-out; use `Pick` for per-session routing.

### E. Config store (only if Q1b said yes)

**Read** the effective config (merged `global < service < version`). On a
registered app, `reg.Config` uses the instance's own service + version:

```go
cfg, err := reg.Config(ctx)               // or reg.Config(ctx, "db/") to filter by prefix
host, _ := cfg.String("db/host")
port, _ := cfg.Int("db/port")
on,   _ := cfg.Bool("feature/x")
var policy MyPolicy
_ = cfg.JSON("policy", &policy)
raw, _ := cfg.Bytes("tls/cert")           // bytes vars decode from base64
```

For an arbitrary service/version (non-registered consumer):

```go
cfg, err := d.ResolveConfig(ctx, "billing", "2.1.0", discovery.WithPrefixes("db/"))
```

Read config **once at startup**; to react to live changes, re-resolve on a
`config.*` event from `d.Watch(ctx)` (or just poll on an interval). Don't build
an elaborate reload framework unless the user asks.

**Write** a block — only when Q1b = "Read + write". Writes are atomic (a new
revision) and need write access to the scope (global → admin):

```go
d.SetServiceConfig(ctx, "billing", map[string]discovery.TypedValue{
    "db/host":   discovery.StringVar("db1"),
    "db/port":   discovery.IntVar(5432),
    "feature/x": discovery.BoolVar(true),
}, "init")
// version-scoped overrides:
d.SetVersionConfig(ctx, "billing", ">=2.1.0", map[string]discovery.TypedValue{
    "db/host": discovery.StringVar("db-fast"),
}, "fast pool for 2.1+")
```

Typed value builders: `StringVar`, `IntVar`, `FloatVar`, `BoolVar`, `BytesVar`,
`JSONVar`. Don't have an app rewrite the *whole* global scope on boot — that
clobbers other operators' keys; apps usually only own their `service:` scope.

---

## Step 4 — env vars

Suggested names (only override if the project already has a different
convention):

| Env | Default | Meaning |
|---|---|---|
| `DISCOVERY_URL` | `http://localhost:8500` | One URL or comma-separated list of discovery2 nodes |
| `DISCOVERY_TOKEN` | (empty) | Bearer token; required if the server has write tokens configured. UI-minted client tokens (`dsc_…`) or static env tokens both work |
| `SERVICE_NAME` | repo / cmd name | logical service this app registers as (also the dashboard client name via `WithName`) |
| `SERVICE_ADDR` | `127.0.0.1` | host or IP peers will use to reach this instance |
| `SERVICE_VERSION` | build tag / `0.1.0` | semver this instance runs; enables version-constrained discovery. Prefer `-ldflags -X` over env |
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
   appeared (the instance should show a `version`). For config, suggest
   `curl 'http://<discovery>/v1/config/resolve?service=<name>&version=<ver>'`
   to see the effective config the app will read. Don't run it yourself unless
   the user explicitly asks.
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
- Doesn't mint client tokens or manage config scopes/ACL/rollbacks — those
  live in the discovery2 UI/admin. The app only reads, and writes config it's
  permitted to (global → admin; service/version → service ACL).

---

## Output style

- If the user writes in Russian, reply in Russian.
- One short paragraph per file changed when reporting back.
- End with: which env vars to set, and what the app does at startup now.
- Don't restate documentation the user can read in the linked README.
