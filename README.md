# discovery2-client

Go client library for [discovery2](https://github.com/axgrid/discovery2) — a small service-discovery service.

```bash
go get github.com/axgrid/ax-discovery2-client
```

## Two things this gives you

1. **Self-registration with auto-heartbeat (or active probing).** Your service tells discovery2 "I'm here at this address, exposing these interfaces". By default a background goroutine keeps the registration alive via heartbeats. Or hand it over to the server with `CheckMode: CheckHTTP` / `CheckTCP` — discovery2 will probe you instead. When you shut down, `Close()` deregisters cleanly.
2. **Resolution + balancing.** `NewResolver` watches a service's instance list over WebSocket and lets you pick one per round-robin / random / weighted strategy.

## Quick start

```go
import (
    "context"
    discovery "github.com/axgrid/ax-discovery2-client"
)

ctx := context.Background()

// Multiple endpoints → automatic failover on network errors.
// WithName makes this client show up by name in the server dashboard.
d := discovery.New("http://disc1:8500,http://disc2:8500",
    discovery.WithToken("write-token"), discovery.WithName("billing"))

// Register self, advertising a version.
reg, err := d.Register(ctx, discovery.Registration{
    Service: "billing",
    Address: "10.0.0.5",
    Version: "2.4.0",
    Interfaces: []discovery.Interface{
        {Name: "WEB",  Protocol: "http", Port: 8080, HealthURL: "/healthz"},
        {Name: "GRPC", Protocol: "tcp",  Port: 9000},
    },
    Weight:     1,
    TTLSeconds: 30,
})
if err != nil { panic(err) }
defer reg.Close()  // deregister on shutdown

// Resolve an upstream service, constrained to a version range.
res, err := d.NewResolver(ctx, "payments", discovery.RoundRobin,
    discovery.WithVersion(">=2.1.0"))
if err != nil { panic(err) }
defer res.Close()

addr, _ := res.PickAddress("WEB")  // "10.0.0.5:8080"
url,  _ := res.PickURL("WEB")      // "http://10.0.0.5:8080/"

// Or let the server pick one instance, sticky by a token (same token → same
// instance until it idles out; re-binds if it dies).
pick, _ := d.Pick(ctx, "payments", discovery.PickOptions{
    Version: ">=2.1.0", Iface: "WEB", Token: sessionID,
})
// pick.Address, pick.URL, pick.Instance, pick.Rebound
```

## API

### Construction

```go
d := discovery.New(baseURLs string, opts ...Option)
```

`baseURLs` is a single URL or comma-separated list of discovery nodes. On a
transport error **or a 5xx response** the client transparently fails over to the
next endpoint (a node behind a proxy often returns 502/503/504 when it's down).

Options:

- `WithToken(t string)` — bearer token sent on every request.
- `WithName(name string)` — identifies this client (`X-Discovery-Client` header); shows up in the server dashboard's request feed and client map.
- `WithEndpointOrder(order)` — `OrderSequential` (default, always tries the first URL first) or `OrderRandom` (random start each request, wraps around — spreads load across nodes). Failover to the rest happens either way.
- `WithRetry(fn)` — override the failover predicate `func(method string, status int, err error) bool`. Default fails over on any transport error or 5xx. Use it to, e.g., only retry idempotent methods.
- `WithCache(backend)` / `WithCacheDir(dir)` — cache read calls (config resolve + discover). Serves fresh-enough entries without a round-trip, revalidates with `If-None-Match` (304 = cheap), and **falls back to the last-known value when every discovery node is down** — so a service can start on its cached config/pool. `WithCacheDir` uses the filesystem; `WithCache` takes any `CacheBackend` (see below).
- `WithCacheTTL(d)` — how long a cached read is served before revalidation (default 30s).
- `WithHTTPClient(*http.Client)` — override timeouts / transport.

### Change detection & caching (ETag)

Reads carry a query-scoped `ETag` — a hash of *exactly* the keys/instances your
request returned. Use it to detect changes cheaply without refetching bodies:

```go
cfg, _ := d.ResolveConfig(ctx, "billing", "2.1.0", discovery.WithPrefixes("db/"))
prev := cfg.ETag

// later — HEAD only, no body:
etag, _ := d.ConfigETag(ctx, "billing", "2.1.0", discovery.WithPrefixes("db/"))
if etag != prev { /* something under db/ changed — re-resolve */ }
```

Large `bytes`/`json` values are hashed once on the server at write time, so this
stays cheap regardless of value size. With `WithCache*`, the client does this
revalidation for you (conditional GET → 304) and survives discovery downtime.

#### Cache backends

`WithCacheDir(dir)` is a filesystem cache. To store the cache elsewhere (a DB,
Redis, …) implement the tiny `CacheBackend` interface:

```go
type CacheBackend interface {
    Get(key string) (data []byte, ok bool)
    Set(key string, data []byte)
}
```

A ready GORM-backed backend ships as a separate module (so GORM isn't a
dependency unless you use it):

```go
import (
    "github.com/glebarez/sqlite"
    "gorm.io/gorm"
    "github.com/axgrid/ax-discovery2-client/gormcache"
)

db, _ := gorm.Open(sqlite.Open("cache.db"), &gorm.Config{})
cache, _ := gormcache.New(db)            // any GORM dialect works
d := discovery.New(url, discovery.WithCache(cache))
```

```sh
go get github.com/axgrid/ax-discovery2-client/gormcache
```

```go
// Three nodes, random pick + failover (incl. 5xx):
d := discovery.New("http://d1:8500,http://d2:8500,http://d3:8500",
    discovery.WithEndpointOrder(discovery.OrderRandom))
```

`Watch` (and the `Resolver`'s live updates) also fails over across endpoints on a dial error.

### Self-registration

```go
type Registration struct {
    Service    string            // required
    ID         string            // optional; UUID generated if empty
    Address    string            // required: host or IP that callers use to reach this instance
    Interfaces []Interface       // ports + protocols (e.g. WEB http:8080, GRPC tcp:9000)
    Weight     int               // for weighted balancing; defaults to 1
    Status     Status            // defaults to "up"
    Metadata   map[string]string // arbitrary labels
    TTLSeconds int               // default 30; if no heartbeat within TTL the instance is marked down

    CheckMode        CheckMode   // default CheckHeartbeat. CheckHTTP / CheckTCP / CheckNone are options.
    CheckIntervalSec int         // probe cadence in seconds (only used for http/tcp); default 15
}

reg, err := d.Register(ctx, reg)
defer reg.Close()
```

`Register` starts a background heartbeat at TTL/3 cadence and sends a delete on `Close()`.

### Liveness modes

- `CheckHeartbeat` (default) — your service stays up by virtue of the auto-heartbeat goroutine. Set `TTLSeconds` to control how fast you go down after a crash.
- `CheckHTTP` — discovery2 probes your HTTP/HTTPS interfaces every `CheckIntervalSec` (15 s default). Set `Interface.HealthURL` (a path or a full URL for proxy-fronted services) to point at a real health endpoint. All HTTP-ish interfaces must pass for the instance to be `up`.
- `CheckTCP` — discovery2 opens a TCP connection to every interface's port. All must succeed.
- `CheckNone` — discovery2 never auto-changes status; manage it via the REST API yourself.

### Resolution & balancing

```go
res, err := d.NewResolver(ctx, "payments", discovery.RoundRobin)
defer res.Close()

inst, _ := res.Pick()                // *Instance
addr, _ := res.PickAddress("GRPC")   // "host:port" for the named interface
url,  _ := res.PickURL("WEB")        // full URL incl. scheme + path
all     := res.Instances()           // snapshot of currently healthy instances
```

Strategies: `RoundRobin`, `Random`, `Weighted`. The resolver subscribes via WebSocket and falls back to a 30s poll if the connection drops. Pass `WithVersion(constraint)` to restrict the cache to a semver range — re-applied on every refresh, so rolling deploys are reflected live.

### Version-aware discovery & sticky pick

```go
// Constraint-filtered instances (npm-style: >=2.1.0, ^2.1, ~2.1.0, 1.x, 1.2.0 - 1.3.5).
d.DiscoverVersion(ctx, "billing", ">=2.1.0")          // []Instance
d.DiscoverAddresses(ctx, "billing", ">=2.1.0", "WEB") // ["10.0.0.5:8080", ...]

// Server-side single pick; Token enables sticky (persisted, cluster-wide) balancing.
d.Pick(ctx, "billing", discovery.PickOptions{Version: "^2.1.0", Iface: "WEB", Token: sessionID})
```

Instances with an empty or non-semver `Version` are excluded when a constraint is supplied.

### Config store

Read merged config (`global < service < version`) and write blocks:

```go
// Read this instance's effective config (uses reg.Service + reg.Version).
cfg, _ := reg.Config(ctx)               // or reg.Config(ctx, "db/") to filter by prefix
host, _ := cfg.String("db/host")
port, _ := cfg.Int("db/port")
on,   _ := cfg.Bool("feature/x")
var policy MyPolicy
_ = cfg.JSON("policy", &policy)

// Or resolve for an arbitrary service/version:
cfg, _ = d.ResolveConfig(ctx, "billing", "2.1.0", discovery.WithPrefixes("db/"))

// Write a block (atomic; creates a new revision). Needs write access to the
// scope (global → admin).
d.SetServiceConfig(ctx, "billing", map[string]discovery.TypedValue{
    "db/host": discovery.StringVar("db1"),
    "db/port": discovery.IntVar(5432),
    "feature/x": discovery.BoolVar(true),
}, "init")
d.SetVersionConfig(ctx, "billing", ">=2.1.0", map[string]discovery.TypedValue{
    "db/host": discovery.StringVar("db-fast"),
}, "fast pool for 2.1+")
```

Typed value builders: `StringVar`, `IntVar`, `FloatVar`, `BoolVar`, `BytesVar`, `JSONVar`.

### Lower-level operations

For UI tools or admin scripts you can call the raw API:

```go
d.ListServices(ctx)
d.PutService(ctx, svc)
d.DeleteService(ctx, "billing")
d.ListInstances(ctx, "billing")
d.Discover(ctx, "billing")           // only "up" instances
d.DeleteInstance(ctx, "billing", id)
d.Heartbeat(ctx, "billing", id, "")  // manual heartbeat
d.Watch(ctx)                         // <-chan Event
```

## Example

The `example/` directory has a runnable demo: registers itself and discovers a peer.

```bash
# Terminal 1 — start the discovery server
git clone https://github.com/axgrid/discovery2.git
cd discovery2 && make run

# Terminal 2 — service A
go run ./example -name billing -port 8080

# Terminal 3 — service B that calls billing
go run ./example -name payments -port 8081 -peer billing
```

## Configuring via env vars

Most apps prefer env-driven config. Conventional pattern:

```go
d := discovery.New(os.Getenv("DISCOVERY_URL"),
    discovery.WithToken(os.Getenv("DISCOVERY_TOKEN")))
```

That's it — discovery2-client itself has no env-var magic, so you stay in control of the names and defaults.

## License

MIT.
