// Command labfwd forwards a local port to a lab device through the controller's
// SOCKS route.
//
// EXPLORATION ONLY, and it exists for one reason: internal/gnmipoll dials with
// gnmic's client, which opens its own TCP connection and has no seam for a
// custom dialer. To exercise the REAL collector against a real switch from a
// host that has no route to the lab, the socket has to be bridged underneath it.
//
// That is exactly what this does and nothing more, which is what makes the test
// worth something: everything above the socket — the SubscribeRequest, the
// encoding, the sub-modes, the translation into samples — is the shipped code
// path, unmodified.
//
// Not part of the product; nothing imports it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"golang.org/x/net/proxy"
)

func main() {
	var listen = flag.String("listen", "127.0.0.1:16030", "local address")
	var target = flag.String("target", "", "device address, e.g. 192.0.2.5:6030")
	var socksAddr = flag.String("socks", "127.0.0.1:1080", "controller SOCKS route")
	flag.Parse()
	if *target == "" {
		fmt.Fprintln(os.Stderr, "usage: labfwd -target IP:PORT [-listen ...] [-socks ...]")
		os.Exit(2)
	}

	var auth *proxy.Auth
	if os.Getenv("SOCKS_USER") != "" {
		auth = &proxy.Auth{
			User:     os.Getenv("SOCKS_USER"),
			Password: os.Getenv("SOCKS_PASS"),
		}
	}
	var dialer, err = proxy.SOCKS5("tcp", *socksAddr, auth, proxy.Direct)
	if err != nil {
		log.Fatalf("socks: %v", err)
	}
	var ln net.Listener
	ln, err = net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("forwarding %s -> %s via %s", *listen, *target, *socksAddr)
	for {
		var local net.Conn
		local, err = ln.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		go func(local net.Conn) {
			defer local.Close() //nolint:errcheck
			var remote, derr = dialer.(proxy.ContextDialer).
				DialContext(context.Background(), "tcp", *target)
			if derr != nil {
				log.Printf("dial %s: %v", *target, derr)
				return
			}
			defer remote.Close() //nolint:errcheck
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = io.Copy(remote, local) }()
			go func() { defer wg.Done(); _, _ = io.Copy(local, remote) }()
			wg.Wait()
		}(local)
	}
}
