package discovery

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Strategy picks which instance to use among healthy ones.
type Strategy int

const (
	// RoundRobin cycles through healthy instances in order.
	RoundRobin Strategy = iota
	// Random picks any healthy instance uniformly.
	Random
	// Weighted picks proportionally to Instance.Weight.
	Weighted
)

// Resolver caches healthy instances of a service and refreshes them via Watch
// (with a 30s poll fallback). It is safe for concurrent use.
type Resolver struct {
	c        *Client
	service  string
	strategy Strategy

	mu        sync.RWMutex
	instances []Instance
	idx       atomic.Uint64

	stop   context.CancelFunc
	closed chan struct{}
}

// NewResolver opens a resolver for the named service. The current instance set
// is fetched immediately, and subsequent changes are picked up in the background.
func (c *Client) NewResolver(ctx context.Context, service string, strategy Strategy) (*Resolver, error) {
	rctx, cancel := context.WithCancel(context.Background())
	r := &Resolver{
		c:        c,
		service:  service,
		strategy: strategy,
		stop:     cancel,
		closed:   make(chan struct{}),
	}
	if err := r.refresh(ctx); err != nil {
		cancel()
		return nil, err
	}
	go r.run(rctx)
	return r, nil
}

// Close stops the background refresh goroutine.
func (r *Resolver) Close() {
	r.stop()
	<-r.closed
}

// Instances returns a snapshot of currently healthy instances.
func (r *Resolver) Instances() []Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Instance, len(r.instances))
	copy(out, r.instances)
	return out
}

// Pick returns one healthy instance per the configured Strategy.
func (r *Resolver) Pick() (*Instance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.instances) == 0 {
		return nil, errors.New("no healthy instances for " + r.service)
	}
	switch r.strategy {
	case Random:
		i := rand.Intn(len(r.instances))
		return &r.instances[i], nil
	case Weighted:
		total := 0
		for _, i := range r.instances {
			w := i.Weight
			if w <= 0 {
				w = 1
			}
			total += w
		}
		pick := rand.Intn(total)
		for _, i := range r.instances {
			w := i.Weight
			if w <= 0 {
				w = 1
			}
			if pick < w {
				return &i, nil
			}
			pick -= w
		}
		return &r.instances[0], nil
	default: // RoundRobin
		n := r.idx.Add(1) - 1
		return &r.instances[n%uint64(len(r.instances))], nil
	}
}

// PickAddress returns "host:port" for the named interface (e.g. "WEB", "GRPC").
// If iface is empty, the first interface on the picked instance is used.
func (r *Resolver) PickAddress(iface string) (string, error) {
	inst, err := r.Pick()
	if err != nil {
		return "", err
	}
	if len(inst.Interfaces) == 0 {
		return inst.Address, nil
	}
	for _, it := range inst.Interfaces {
		if iface == "" || it.Name == iface {
			return fmt.Sprintf("%s:%d", inst.Address, it.Port), nil
		}
	}
	return "", fmt.Errorf("instance %s has no interface %q", inst.ID, iface)
}

// PickURL returns "scheme://host:port/path" for an HTTP / WS interface.
func (r *Resolver) PickURL(iface string) (string, error) {
	inst, err := r.Pick()
	if err != nil {
		return "", err
	}
	for _, it := range inst.Interfaces {
		if iface != "" && it.Name != iface {
			continue
		}
		scheme := it.Protocol
		if it.TLS {
			scheme += "s"
		}
		return fmt.Sprintf("%s://%s:%d%s", scheme, inst.Address, it.Port, it.Path), nil
	}
	return "", fmt.Errorf("no matching interface")
}

func (r *Resolver) refresh(ctx context.Context) error {
	insts, err := r.c.Discover(ctx, r.service)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.instances = insts
	r.mu.Unlock()
	return nil
}

func (r *Resolver) run(ctx context.Context) {
	defer close(r.closed)
	poll := time.NewTicker(30 * time.Second)
	defer poll.Stop()

	var watchCh <-chan Event
	for {
		if watchCh == nil {
			ch, err := r.c.Watch(ctx)
			if err == nil {
				watchCh = ch
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			_ = r.refresh(ctx)
		case ev, ok := <-watchCh:
			if !ok {
				watchCh = nil
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			if ev.Service != r.service {
				continue
			}
			_ = r.refresh(ctx)
		}
	}
}
