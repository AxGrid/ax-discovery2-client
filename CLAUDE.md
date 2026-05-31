# CLAUDE.md

Operational notes for Claude Code working in **discovery2-client** — the Go
client library for [discovery2](https://github.com/axgrid/discovery2). Read
this once at session start.

For the user-facing description, see [`README.md`](README.md). The companion
server lives at `../discovery2` (locally) / `github.com/axgrid/discovery2`.

---

## Module shape

Single-package module. Import path: `github.com/axgrid/discovery2-client`.
Package name: `discovery`. Conventional alias:

```go
import discovery "github.com/axgrid/discovery2-client"
```

Files:

```
go.mod                # module github.com/axgrid/discovery2-client; go 1.23
types.go              # Service, Instance, Interface, Status, Event + constants
client.go             # Client, Option (WithToken/WithName/WithHTTPClient), CRUD/Discover/Heartbeat,
                      # DiscoverVersion/DiscoverAddresses/Pick (semver + sticky),
                      # Register + Registered (auto-heartbeat handle), Watch
resolver.go           # Resolver, Strategy (RoundRobin/Random/Weighted),
                      # ResolverOption (WithVersion), Pick/PickAddress/PickURL;
                      # refreshes via Watch + 30s poll
README.md             # public docs
example/main.go       # runnable demo: registers self, optionally calls a peer
```

External deps: `github.com/google/uuid`, `github.com/gorilla/websocket`. Keep
this list short — every new dep here is one every consumer pays for.

---

## Wire types are duplicated on purpose

`types.go` mirrors the `model.*` types in the server's `internal/model`. The
server keeps its types `internal/` so external code can't import them; the
client therefore has its own copy. **Drift between the two is a bug** —
specifically, the JSON tag names and field types must match exactly, since the
contract is the wire format.

Currently mirrored:

- `Service`, `Interface`, `Instance` — main resources. `Instance.Version`
  (semver, registrable) and `Instance.Blocked` (operator kill-switch,
  read-only here) are part of the contract.
- `Status`, `Visibility`, `CheckMode` — enum-ish string types
- `ProbeResult`, `InstanceCheck` — per-interface health probe report
- `Event` — WebSocket change notification
- Constants: `Status*`, `Check*`, `Event*`

When the server's wire format changes:

1. Update `types.go` here to match (field names, JSON tags, types).
2. If the new field is settable on registration, add it to the `Registration`
   struct in `client.go` and copy it into the `Instance` literal inside
   `Register`. (See `CheckMode` / `CheckIntervalSec` for the template.)
3. Bump the major version of the server's contract if a field becomes
   required or its semantics change. Adding new optional fields is safe
   (Go decodes unknown fields as zero values).
4. Re-run `go build ./... && go vet ./...` in both repos.

---

## Two abstractions to know

### `Client` — raw + helpers

`Client.do` is the one-shot HTTP path with multi-endpoint failover (try each
`baseURL` in order until one returns a non-network error). It does *not*
retry on HTTP 5xx; that's a deliberate choice because retries on 5xx without
idempotency keys can double-write.

`Register` is the high-level flow: PUT the instance, then conditionally start
a goroutine that calls `Heartbeat` at TTL/3 cadence. Returns a `*Registered`
whose `Close` both stops the heartbeat goroutine (if running) AND deletes
the instance from the server. Always `defer reg.Close()`. Set
`Registration.Version` (semver) so other callers can filter by it.

**`WithName(name)` is recommended.** It sends `X-Discovery-Client: <name>` on
every request; the server's dashboard groups its request feed and client map
by that name (otherwise it falls back to the caller's IP). Use your service
name, optionally suffixed with an instance id.

**Version-aware discovery.** `DiscoverVersion(service, constraint)` and
`DiscoverAddresses(service, constraint, iface)` apply an npm-style semver
filter server-side (`>=2.1.0`, `^2.1`, `~2.1.0`, `1.x`, `1.2.0 - 1.3.5`).
Instances with an empty/non-semver `Version` are excluded under a constraint.

**`Pick(service, PickOptions)` is server-side selection.** Use it instead of a
long-lived `Resolver` when you want sticky balancing: pass `PickOptions.Token`
and repeated picks land on the same instance until it idles past the server's
affinity TTL; if that instance dies the server re-binds and sets
`PickResult.Rebound`. `PickOptions.Version`/`Iface` mirror the discover params.

**The heartbeat goroutine runs only when `Registration.CheckMode` is
`CheckHeartbeat` (or empty).** For `CheckHTTP` / `CheckTCP` the server
polls the instance directly; sending heartbeats would be wasted work
(harmless but pointless). For `CheckNone` the caller manages status by hand.

**`Registration.Tags` is best-effort.** When non-empty, `Register` first
calls `ensureServiceTags` which GETs the parent Service, merges the
provided tags with the existing ones, and PUTs the result. A failure here
does NOT abort the registration — by design, an instance always wins over
a tag-sync glitch. Description follows the same merge pattern but is only
written when the service has none yet (we never overwrite operator-set
descriptions). If a caller absolutely needs the service-level write to
land before registering, they should call `PutService` themselves first.

**`Register` always stamps `Managed: true` on the wire payload.** The
server uses that flag to block UI-session edits/deletes of the instance
(only the same client coming back via the same static token can mutate
it). Operators can still see and Check the instance in the UI, but the
Edit and Delete buttons are hidden — preventing the case where a human
changes a field that the client will silently overwrite on its next
restart. There is no opt-out: if you call `Register`, the instance is
managed. Operators wanting hand-edited instances should create them via
the UI / `PutInstance` instead.

### `Resolver` — caches + balances

`NewResolver` does an initial `Discover` synchronously (so the caller knows
right away whether the service exists), then starts a background goroutine
that:

- subscribes via WebSocket `/v1/watch` and refreshes on relevant events;
- falls back to a 30 s poll if the watch drops;
- reconnects WS with a 2 s backoff.

`Pick` returns one healthy instance per `Strategy`. `PickAddress(iface)` and
`PickURL(iface)` are sugar for the two call patterns we expect (gRPC dial
target vs HTTP base URL). `Instances()` returns a snapshot if the caller
wants to balance manually.

Pass `WithVersion(constraint)` to `NewResolver` to restrict the cache to
instances satisfying a semver constraint; the filter is re-applied on every
refresh, so a rolling deploy is reflected live. The option is variadic and
backward-compatible — existing `NewResolver(ctx, svc, strategy)` calls are
unaffected.

Don't cache the result of `PickAddress` past one request — instances change
under you.

---

## Commands

```bash
go build ./...
go vet ./...
go test ./...

# Run the example against a local server:
go run ./example -name billing -port 8080
go run ./example -name payments -port 8081 -peer billing
```

`go.sum` should be regenerated by `go mod tidy` after dep changes; never edit
it by hand.

---

## Conventions

- **Errors** wrap with `fmt.Errorf("discovery: %s", ...)` so callers can grep
  for the prefix; we don't expose error sentinels (no `errors.Is` cases) since
  the API is HTTP and most errors are transport-level.
- **Context** is the first arg of every public method that does I/O. Internal
  helpers omit `ctx` only when they truly don't.
- **No global state**, no init goroutines, no panics on bad config — return
  errors and let the caller decide.
- **No retries with backoff** in the public API. The resolver re-fetches on
  watch reconnect; that's the only retry-shaped behaviour.
- **No metrics / tracing wrappers built in.** Callers wrap `*http.Client`
  via `WithHTTPClient` if they want instrumentation.
- **Comments**: only when behaviour is non-obvious (e.g. why we re-marshal
  the body on each retry inside `do`). Don't restate identifier names.

---

## Adding new features

### A new server endpoint just shipped

1. Add the wire types to `types.go` if any are new.
2. Add a `Client` method that calls it. Mirror the existing CRUD methods —
   path-escape user input, use `decodeJSON`, pass `ctx` through.
3. If it's a watchable event type, add the constant to `types.go` and verify
   the resolver's filter logic still does the right thing.
4. Update `README.md` if the new method belongs in the high-level pitch.
5. Update `~/.claude/skills/ax-discovery2-client/SKILL.md` if the integration
   recipe changes (e.g. a new init step the consumer needs to call).

### A new resolver strategy

1. Add the constant to the `Strategy` block in `resolver.go`.
2. Implement the case inside `Pick`'s switch.
3. The strategy must be safe under `Pick` being called concurrently — the
   existing implementations either lock-free atomic increment (RoundRobin) or
   read-locked over a stable snapshot.

### A new Option

Add it to `client.go` next to `WithToken` / `WithHTTPClient`. Keep options
side-effect-free; they should mutate the `*Client` and nothing else.

---

## Releasing

1. Tag with semver: `git tag v0.X.0 && git push origin v0.X.0`.
2. Test consumers can `go get github.com/axgrid/discovery2-client@v0.X.0`.
3. Update the version mention in the `/ax-discovery2-client` skill if the
   skill recommends pinning a version.

Don't do `replace` directives in `go.mod`; the module must be importable
straight from GitHub.
