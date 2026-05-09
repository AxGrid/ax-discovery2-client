// Example service: registers itself with discovery, serves a tiny HTTP endpoint,
// and discovers a peer service via the resolver.
//
// Run two of these against a discovery server to see registration + balancing:
//
//	go run ./example -name billing  -port 8080
//	go run ./example -name payments -port 8081 -peer billing
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	discovery "github.com/axgrid/discovery2-client"
)

func main() {
	var (
		discoveryURL = flag.String("discovery", "http://localhost:8500", "discovery server URL(s), comma-separated for failover")
		token        = flag.String("token", "", "discovery write token")
		name         = flag.String("name", "demo", "this service name")
		addr         = flag.String("addr", "127.0.0.1", "this instance address")
		port         = flag.Int("port", 8080, "this instance port")
		peer         = flag.String("peer", "", "optional peer service to call")
	)
	flag.Parse()

	d := discovery.New(*discoveryURL, discovery.WithToken(*token))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg, err := d.Register(ctx, discovery.Registration{
		Service: *name,
		Address: *addr,
		Interfaces: []discovery.Interface{
			{Name: "WEB", Protocol: "http", Port: *port, HealthURL: "/healthz"},
		},
		TTLSeconds: 30,
	})
	if err != nil {
		log.Fatalf("register: %v", err)
	}
	defer reg.Close()
	log.Printf("registered as %s/%s", *name, reg.ID)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s/%s\n", *name, reg.ID)
	})
	go func() {
		log.Printf("HTTP :%d", *port)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
			log.Printf("http: %v", err)
		}
	}()

	if *peer != "" {
		go callPeer(ctx, d, *peer)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
}

func callPeer(ctx context.Context, d *discovery.Client, peer string) {
	res, err := d.NewResolver(ctx, peer, discovery.RoundRobin)
	if err != nil {
		log.Printf("resolver(%s): %v", peer, err)
		return
	}
	defer res.Close()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if url, err := res.PickURL("WEB"); err == nil {
				log.Printf("would call %s → %s", peer, url)
			} else {
				log.Printf("no peer yet: %v", err)
			}
		}
	}
}
