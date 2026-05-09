// Package discovery is the Go client library for the discovery2 service.
//
// Typical usage in a service that wants to register itself:
//
//	d := discovery.New("http://discovery:8500", discovery.WithToken("write-token"))
//	reg, err := d.Register(ctx, discovery.Registration{
//	    Service: "billing",
//	    Address: "10.0.0.5",
//	    Interfaces: []discovery.Interface{
//	        {Name: "WEB", Protocol: "http", Port: 8080, HealthURL: "/healthz"},
//	        {Name: "GRPC", Protocol: "tcp", Port: 9000},
//	    },
//	    TTLSeconds: 30,
//	})
//	defer reg.Close()
//
// Typical usage in a service that wants to call another service:
//
//	res, err := d.NewResolver(ctx, "billing", discovery.RoundRobin)
//	addr, _ := res.PickAddress("WEB") // "10.0.0.5:8080"
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Client talks to one or more discovery2 servers.
type Client struct {
	endpoints []string
	token     string
	hc        *http.Client
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithToken sets the bearer token sent on every request.
func WithToken(t string) Option { return func(c *Client) { c.token = t } }

// WithHTTPClient overrides the underlying http.Client (timeouts, transport, etc).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.hc = h } }

// New constructs a Client. baseURLs may be a single URL or a comma-separated
// list of URLs to several discovery nodes; on a network error the Client
// transparently retries the next endpoint.
func New(baseURLs string, opts ...Option) *Client {
	c := &Client{
		endpoints: splitURLs(baseURLs),
		hc:        &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func splitURLs(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// do tries each endpoint in order until one returns a non-network error.
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var lastErr error
	for _, ep := range c.endpoints {
		var rdr io.Reader
		if raw != nil {
			rdr = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, ep+path, rdr)
		if err != nil {
			return nil, err
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no discovery endpoints configured")
	}
	return nil, lastErr
}

func decodeJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("discovery: %s", e.Error)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- service & instance ops ---

// ListServices returns all known services. Pass an empty tag for "no filter";
// any non-empty tag is forwarded as ?tag=<...> and limits the result to
// services that carry that tag.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	return c.ListServicesByTag(ctx, "")
}

// ListServicesByTag returns services that carry the given tag. Pass an empty
// string to fetch every service.
func (c *Client) ListServicesByTag(ctx context.Context, tag string) ([]Service, error) {
	path := "/v1/services"
	if tag != "" {
		path += "?tag=" + url.QueryEscape(tag)
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out []Service
	return out, decodeJSON(resp, &out)
}

// TagCount is the {tag, service-count} row returned by ListTags.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// ListTags returns every distinct tag in use, with a service-count for each.
func (c *Client) ListTags(ctx context.Context) ([]TagCount, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/tags", nil)
	if err != nil {
		return nil, err
	}
	var out []TagCount
	return out, decodeJSON(resp, &out)
}

// DiscoverByTag returns up-instances of every service that carries the tag.
// Useful for "give me anything that exposes the 'cache' role".
func (c *Client) DiscoverByTag(ctx context.Context, tag string) ([]Instance, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/discover?tag="+url.QueryEscape(tag), nil)
	if err != nil {
		return nil, err
	}
	var out []Instance
	return out, decodeJSON(resp, &out)
}

func (c *Client) PutService(ctx context.Context, svc Service) (*Service, error) {
	resp, err := c.do(ctx, http.MethodPut, "/v1/services/"+url.PathEscape(svc.Name), svc)
	if err != nil {
		return nil, err
	}
	var out Service
	return &out, decodeJSON(resp, &out)
}

// GetService fetches one service definition.
func (c *Client) GetService(ctx context.Context, name string) (*Service, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, err
	}
	var out Service
	return &out, decodeJSON(resp, &out)
}

// ensureServiceTags merges tags (and an optional description) into the parent
// Service. Existing tags are preserved; the function never overwrites a
// description that the operator may have set in the UI.
//
// Failures here are non-fatal for Register; the caller logs and ignores.
func (c *Client) ensureServiceTags(ctx context.Context, name string, tags []string, desc string) error {
	current, err := c.GetService(ctx, name)
	merged := append([]string(nil), tags...)
	currentDesc := ""
	if err == nil {
		currentDesc = current.Description
		seen := make(map[string]bool, len(merged))
		for _, t := range merged {
			seen[t] = true
		}
		// Append existing tags not already in the merge.
		for _, t := range current.Tags {
			if !seen[t] {
				merged = append(merged, t)
				seen[t] = true
			}
		}
		// If the service already has the same tag set and description, skip the
		// PUT — it would be a needless audit-log entry.
		if sameStringSet(current.Tags, merged) && (desc == "" || currentDesc != "") {
			return nil
		}
	}
	body := map[string]any{"tags": merged}
	if currentDesc == "" && desc != "" {
		body["description"] = desc
	}
	resp, err := c.do(ctx, http.MethodPut, "/v1/services/"+url.PathEscape(name), body)
	if err != nil {
		return err
	}
	return decodeJSON(resp, nil)
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			return false
		}
	}
	return true
}

func (c *Client) DeleteService(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/v1/services/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	return decodeJSON(resp, nil)
}

func (c *Client) ListInstances(ctx context.Context, service string) ([]Instance, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(service)+"/instances", nil)
	if err != nil {
		return nil, err
	}
	var out []Instance
	return out, decodeJSON(resp, &out)
}

// Discover returns only "up" instances of the named service.
func (c *Client) Discover(ctx context.Context, service string) ([]Instance, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/discover/"+url.PathEscape(service), nil)
	if err != nil {
		return nil, err
	}
	var out []Instance
	return out, decodeJSON(resp, &out)
}

func (c *Client) DeleteInstance(ctx context.Context, service, id string) error {
	resp, err := c.do(ctx, http.MethodDelete,
		"/v1/services/"+url.PathEscape(service)+"/instances/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return decodeJSON(resp, nil)
}

// Heartbeat extends an instance's TTL and optionally updates its Status.
// Pass an empty Status to leave it unchanged.
func (c *Client) Heartbeat(ctx context.Context, service, id string, status Status) (*Instance, error) {
	body := map[string]string{}
	if status != "" {
		body["status"] = string(status)
	}
	resp, err := c.do(ctx, http.MethodPost,
		"/v1/services/"+url.PathEscape(service)+"/instances/"+url.PathEscape(id)+"/heartbeat", body)
	if err != nil {
		return nil, err
	}
	var out Instance
	return &out, decodeJSON(resp, &out)
}

// --- registration with auto-heartbeat ---

// Registration describes the instance you want to register.
type Registration struct {
	Service    string            // required
	ID         string            // optional; UUID generated if empty
	Address    string            // required: host or IP that callers will use to reach this instance
	Interfaces []Interface       // ports and protocols this instance exposes
	Weight     int               // for weighted balancing; defaults to 1
	Status     Status            // defaults to "up"
	Metadata   map[string]string // arbitrary labels
	TTLSeconds int               // default 30; the instance is marked down if no heartbeat within TTL

	// CheckMode picks the liveness strategy. Defaults to CheckHeartbeat — the
	// auto-heartbeat goroutine started by Register keeps the instance up.
	// Set to CheckHTTP / CheckTCP if you want the server to actively probe
	// instead, or CheckNone to manage status yourself.
	CheckMode        CheckMode
	CheckIntervalSec int // probe cadence in seconds; default 15 (only used for http/tcp modes)

	// Tags are written onto the parent Service definition so that operators and
	// other clients can filter services by tag (DiscoverByTag, ListTags).
	// Existing tags on the service are preserved — Register only adds the ones
	// that aren't already there. Leave empty to skip the service-level write.
	Tags []string

	// Description is set on the parent Service when non-empty AND no description
	// is yet recorded. We never overwrite a description set elsewhere.
	Description string
}

// Registered is the handle returned by Register. Call Close() during shutdown
// to deregister and stop the heartbeat goroutine.
type Registered struct {
	Service string
	ID      string
	c       *Client
	cancel  context.CancelFunc
	done    chan struct{}
}

// Close deregisters the instance and stops the heartbeat goroutine.
func (r *Registered) Close() error {
	r.cancel()
	<-r.done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.c.DeleteInstance(ctx, r.Service, r.ID)
}

// Register creates an instance and starts a background heartbeat loop at TTL/3 cadence.
func (c *Client) Register(ctx context.Context, reg Registration) (*Registered, error) {
	if reg.Service == "" {
		return nil, errors.New("discovery: Service is required")
	}
	if reg.Address == "" {
		return nil, errors.New("discovery: Address is required")
	}
	if reg.ID == "" {
		reg.ID = uuid.NewString()
	}
	if reg.TTLSeconds == 0 {
		reg.TTLSeconds = 30
	}
	if reg.Status == "" {
		reg.Status = StatusUp
	}

	if len(reg.Tags) > 0 || reg.Description != "" {
		if err := c.ensureServiceTags(ctx, reg.Service, reg.Tags, reg.Description); err != nil {
			// Tag/description sync is best-effort; do not abort the registration.
			// Callers that strictly require the service-level write should call
			// PutService themselves.
			_ = err
		}
	}

	inst := Instance{
		ID:               reg.ID,
		ServiceName:      reg.Service,
		Address:          reg.Address,
		Interfaces:       reg.Interfaces,
		Weight:           reg.Weight,
		Status:           reg.Status,
		Metadata:         reg.Metadata,
		TTLSeconds:       reg.TTLSeconds,
		CheckMode:        reg.CheckMode,
		CheckIntervalSec: reg.CheckIntervalSec,
	}
	resp, err := c.do(ctx, http.MethodPut,
		"/v1/services/"+url.PathEscape(reg.Service)+"/instances/"+url.PathEscape(reg.ID), inst)
	if err != nil {
		return nil, err
	}
	if err := decodeJSON(resp, &inst); err != nil {
		return nil, err
	}

	hbCtx, cancel := context.WithCancel(context.Background())
	r := &Registered{
		Service: reg.Service,
		ID:      reg.ID,
		c:       c,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	// Heartbeats are only meaningful in heartbeat mode. For active-probe modes
	// the server polls us; sending heartbeats does no harm but it's wasted work.
	if reg.CheckMode == "" || reg.CheckMode == CheckHeartbeat {
		go r.heartbeatLoop(hbCtx, time.Duration(reg.TTLSeconds)*time.Second/3)
	} else {
		close(r.done) // nothing to wait for on Close
	}
	return r, nil
}

func (r *Registered) heartbeatLoop(ctx context.Context, every time.Duration) {
	defer close(r.done)
	if every < 1*time.Second {
		every = 1 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = r.c.Heartbeat(hbCtx, r.Service, r.ID, "")
			cancel()
		}
	}
}

// --- watch ---

// Watch streams change events over WebSocket. The returned channel closes
// when the connection drops or ctx is cancelled.
//
// For a long-lived subscription use NewResolver, which reconnects automatically.
func (c *Client) Watch(ctx context.Context) (<-chan Event, error) {
	if len(c.endpoints) == 0 {
		return nil, errors.New("no endpoints")
	}
	ep := c.endpoints[0]
	wsURL := strings.Replace(ep, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1) + "/v1/watch"
	hdr := http.Header{}
	if c.token != "" {
		hdr.Set("Authorization", "Bearer "+c.token)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, hdr)
	if err != nil {
		return nil, err
	}
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		defer conn.Close()
		for {
			var ev Event
			if err := conn.ReadJSON(&ev); err != nil {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
