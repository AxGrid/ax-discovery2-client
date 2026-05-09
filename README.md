# discovery2-client

Go client library for [discovery2](https://github.com/axgrid/discovery2) — a small service-discovery service.

```bash
go get github.com/axgrid/discovery2-client
```

## Two things this gives you

1. **Self-registration with auto-heartbeat (or active probing).** Your service tells discovery2 "I'm here at this address, exposing these interfaces". By default a background goroutine keeps the registration alive via heartbeats. Or hand it over to the server with `CheckMode: CheckHTTP` / `CheckTCP` — discovery2 will probe you instead. When you shut down, `Close()` deregisters cleanly.
2. **Resolution + balancing.** `NewResolver` watches a service's instance list over WebSocket and lets you pick one per round-robin / random / weighted strategy.

## Quick start

```go
import (
    "context"
    discovery "github.com/axgrid/discovery2-client"
)

ctx := context.Background()

// Multiple endpoints → automatic failover on network errors.
d := discovery.New("http://disc1:8500,http://disc2:8500",
    discovery.WithToken("write-token"))

// Register self.
reg, err := d.Register(ctx, discovery.Registration{
    Service: "billing",
    Address: "10.0.0.5",
    Interfaces: []discovery.Interface{
        {Name: "WEB",  Protocol: "http", Port: 8080, HealthURL: "/healthz"},
        {Name: "GRPC", Protocol: "tcp",  Port: 9000},
    },
    Weight:     1,
    TTLSeconds: 30,
})
if err != nil { panic(err) }
defer reg.Close()  // deregister on shutdown

// Resolve an upstream service.
res, err := d.NewResolver(ctx, "payments", discovery.RoundRobin)
if err != nil { panic(err) }
defer res.Close()

addr, _ := res.PickAddress("WEB")  // "10.0.0.5:8080"
url,  _ := res.PickURL("WEB")      // "http://10.0.0.5:8080/"
```

## API

### Construction

```go
d := discovery.New(baseURLs string, opts ...Option)
```

`baseURLs` is a single URL or comma-separated list. On a network error, the client transparently retries the next endpoint.

Options:

- `WithToken(t string)` — bearer token sent on every request.
- `WithHTTPClient(*http.Client)` — override timeouts / transport.

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

Strategies: `RoundRobin`, `Random`, `Weighted`. The resolver subscribes via WebSocket and falls back to a 30s poll if the connection drops.

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
