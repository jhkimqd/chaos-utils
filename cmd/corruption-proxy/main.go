// corruption-proxy is a standalone HTTP reverse proxy that applies semantic
// corruption rules to JSON responses. It parses and mutates JSON using the
// full standard library, with support for stateful rules and a control API.
//
// # Usage
//
//	corruption-proxy \
//	  --listen  :16317 \
//	  --target  1317   \
//	  --rules   /tmp/corruption-rules.yaml \
//	  --control :16318
//
// # Sidecar integration
//
// Dockerfile.chaos-utils already builds and installs this binary at
// /usr/local/bin/corruption-proxy in the sidecar image. The injector
// (pkg/injection/injector.go injectCorruptionProxy) starts it in each
// target's sidecar via ExecInSidecar:
//
//	corruption-proxy --listen :<proxyPort> --target <targetPort> \
//	  --rules /tmp/corruption-rules-<targetPort>.yaml \
//	  --control :<proxyPort+1> &
//
// # iptables redirect
//
// The proxy relies on the same iptables PREROUTING REDIRECT rule already used
// by the Envoy-based injector:
//
//	iptables -t nat -A PREROUTING \
//	  -p tcp --dport <targetPort> \
//	  -j REDIRECT --to-port <proxyPort> \
//	  -m comment --comment chaos-corruption-proxy
//
// The proxy listens on proxyPort (= 15000 + targetPort, matching the existing
// convention in pkg/injection/http/http_fault.go) and forwards all traffic to
// 127.0.0.1:<targetPort>.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/injection/http/corruption"
)

func main() {
	var (
		listenAddr  = flag.String("listen", ":15000", "Address for the corruption proxy to listen on (e.g. :16317)")
		targetPort  = flag.Int("target", 0, "Port of the upstream service on 127.0.0.1 (required)")
		rulesFile   = flag.String("rules", "", "Path to the YAML corruption rules file (required)")
		controlAddr = flag.String("control", "127.0.0.1:15001", "Address for the control API (e.g. 127.0.0.1:16318)")
	)
	flag.Parse()

	if *targetPort <= 0 {
		fmt.Fprintln(os.Stderr, "error: --target port is required and must be > 0")
		flag.Usage()
		os.Exit(1)
	}
	if *rulesFile == "" {
		fmt.Fprintln(os.Stderr, "error: --rules path is required")
		flag.Usage()
		os.Exit(1)
	}

	// Load initial rules.
	ruleData, err := os.ReadFile(*rulesFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to read rules file %q: %v\n", *rulesFile, err)
		os.Exit(1)
	}

	rules := &corruption.RuleSet{}
	if err := rules.Load(ruleData); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid rules file: %v\n", err)
		os.Exit(1)
	}

	targetURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", *targetPort),
	}

	proxy := corruption.NewProxy(targetURL, rules)

	// Start control API in the background.
	ctrl := corruption.NewControlServer(rules, proxy)
	go func() {
		fmt.Printf("[CORRUPT] control API listening on %s\n", *controlAddr)
		if err := http.ListenAndServe(*controlAddr, ctrl.Handler()); err != nil {
			fmt.Fprintf(os.Stderr, "[CORRUPT] control API error: %v\n", err)
		}
	}()

	fmt.Printf("[CORRUPT] proxy listening on %s → 127.0.0.1:%d (rules: %s)\n",
		*listenAddr, *targetPort, *rulesFile)

	srv := &http.Server{Addr: *listenAddr, Handler: proxy}
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		log.Println("[SHUTDOWN] received signal, draining connections...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx) //nolint:errcheck
	}()
	log.Fatal(srv.ListenAndServe())
}
