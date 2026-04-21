// Smoke harness for http.HTTPFaultWrapper against a live kurtosis-pos bor
// container. Targets port 8545 (bor's JSON-RPC HTTP endpoint) and verifies
// that responses are actually intercepted by Envoy + iptables REDIRECT.
//
// Usage: go run ./cmd/smoke-http <containerID>
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/http"
	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
)

const sidecarImage = "jhkimqd/chaos-utils:latest"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: smoke-http <containerID>")
		os.Exit(2)
	}
	cid := os.Args[1]

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli, err := docker.New()
	if err != nil {
		fmt.Printf("new client: %v\n", err)
		os.Exit(1)
	}
	mgr := sidecar.New(cli, sidecarImage)
	hw := http.New(mgr)

	if _, err := mgr.CreateSidecar(ctx, cid); err != nil {
		fmt.Printf("create sidecar: %v\n", err)
		os.Exit(1)
	}
	defer mgr.DestroySidecar(ctx, cid)

	// Baseline: request through the sidecar's shared netns against 127.0.0.1:8545
	fmt.Println("=== baseline: curl -X POST 127.0.0.1:8545 eth_blockNumber ===")
	out, _ := mgr.ExecInSidecar(ctx, cid, []string{"sh", "-c",
		`curl -s -X POST -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}' http://127.0.0.1:8545 | head -c 200; echo`})
	fmt.Print(out)

	fmt.Println("\n=== iptables -t nat -L PREROUTING -n (baseline) ===")
	out, _ = mgr.ExecInSidecar(ctx, cid, []string{"iptables", "-t", "nat", "-L", "PREROUTING", "-n"})
	fmt.Print(out)

	// Inject: abort 100% of responses with HTTP 503
	fmt.Println("\n=== INJECT http_fault: abort 100% with HTTP 503 on port 8545 ===")
	t0 := time.Now()
	params := http.HTTPFaultParams{
		TargetPort:   8545,
		AbortCode:    503,
		AbortPercent: 100,
	}
	if err := hw.InjectHTTPFault(ctx, cid, params); err != nil {
		fmt.Printf("INJECT FAILED after %v: %v\n", time.Since(t0), err)
		return
	}
	fmt.Printf("Inject returned after %v\n", time.Since(t0))

	time.Sleep(1 * time.Second)
	fmt.Println("\n=== post-inject: iptables nat PREROUTING ===")
	out, _ = mgr.ExecInSidecar(ctx, cid, []string{"iptables", "-t", "nat", "-L", "PREROUTING", "-n"})
	fmt.Print(out)

	fmt.Println("\n=== post-inject: curl should return 503 from Envoy ===")
	// Use -w to show the HTTP status regardless of body
	out, _ = mgr.ExecInSidecar(ctx, cid, []string{"sh", "-c",
		`curl -s -o /tmp/body -w 'HTTP %{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}' http://127.0.0.1:8545 ; echo '---body:---'; head -c 300 /tmp/body`})
	fmt.Print(out)

	fmt.Println("\n\n=== REMOVE ===")
	if err := hw.RemoveFault(ctx, cid, params); err != nil {
		fmt.Printf("REMOVE FAILED: %v\n", err)
	}
	time.Sleep(1 * time.Second)

	fmt.Println("\n=== post-remove: iptables PREROUTING (should be empty) ===")
	out, _ = mgr.ExecInSidecar(ctx, cid, []string{"iptables", "-t", "nat", "-L", "PREROUTING", "-n"})
	fmt.Print(out)

	fmt.Println("\n=== post-remove: curl should return bor's real response ===")
	out2, _ := mgr.ExecInSidecar(ctx, cid, []string{"sh", "-c",
		`curl -s -o /tmp/body -w 'HTTP %{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}' http://127.0.0.1:8545 ; echo '---body:---'; head -c 200 /tmp/body`})
	fmt.Print(out2)
}
