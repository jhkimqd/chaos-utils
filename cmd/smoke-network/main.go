// Smoke-test harness for network fault injectors (tc netem, DNS, iptables)
// against a live kurtosis-pos container. Exercises each wrapper directly and
// prints the visible evidence (tc qdisc show, iptables -L, etc.) so the
// operator can see whether the fault actually landed.
//
// Usage:
//
//	go run ./cmd/smoke-network <containerID> <mode>
//
// modes: netem | dns | conndrop | all
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/dns"
	"github.com/jihwankim/chaos-utils/pkg/injection/firewall"
	"github.com/jihwankim/chaos-utils/pkg/injection/l3l4"
	"github.com/jihwankim/chaos-utils/pkg/injection/sidecar"
)

const sidecarImage = "jhkimqd/chaos-utils:latest"

func sample(ctx context.Context, mgr *sidecar.Manager, cid, tag string) {
	fmt.Printf("\n--- %s: tc qdisc show dev eth0 ---\n", tag)
	out, err := mgr.ExecInSidecar(ctx, cid, []string{"tc", "qdisc", "show", "dev", "eth0"})
	fmt.Print(out)
	if err != nil {
		fmt.Printf("(err: %v)\n", err)
	}
	fmt.Printf("--- %s: tc filter show dev eth0 ---\n", tag)
	out2, _ := mgr.ExecInSidecar(ctx, cid, []string{"tc", "filter", "show", "dev", "eth0"})
	fmt.Print(out2)
	fmt.Printf("--- %s: iptables -L -n (filter) ---\n", tag)
	out3, _ := mgr.ExecInSidecar(ctx, cid, []string{"iptables", "-L", "-n"})
	fmt.Print(out3)
	fmt.Printf("--- %s: ping -c 3 8.8.8.8 ---\n", tag)
	out4, _ := mgr.ExecInSidecar(ctx, cid, []string{"sh", "-c", "ping -W 2 -c 3 8.8.8.8 2>&1 | tail -5"})
	fmt.Print(out4)
}

func testNetem(ctx context.Context, mgr *sidecar.Manager, cid string) {
	tw := l3l4.NewTCWrapper(mgr)
	fmt.Println("\n========== NETEM (100ms delay + 10% loss, whole-device) ==========")
	if err := tw.InjectFault(ctx, cid, l3l4.FaultParams{Latency: 100, PacketLoss: 10}); err != nil {
		fmt.Printf("INJECT FAILED: %v\n", err)
		return
	}
	sample(ctx, mgr, cid, "netem injected")

	if err := tw.RemoveFault(ctx, cid); err != nil {
		fmt.Printf("REMOVE FAILED: %v\n", err)
	}
	sample(ctx, mgr, cid, "netem removed")
}

func testDNS(ctx context.Context, mgr *sidecar.Manager, cid string) {
	dw := dns.New(mgr)
	fmt.Println("\n========== DNS (500ms delay on udp/53) ==========")
	if err := dw.InjectDNSDelay(ctx, cid, dns.DNSParams{DelayMs: 500}); err != nil {
		fmt.Printf("INJECT FAILED: %v\n", err)
		return
	}
	sample(ctx, mgr, cid, "dns injected")

	fmt.Println("--- dns: resolve example.com (should show ~500ms delay) ---")
	out, _ := mgr.ExecInSidecar(ctx, cid, []string{"sh", "-c", "time nslookup example.com 2>&1 | tail -10"})
	fmt.Print(out)

	if err := dw.RemoveFault(ctx, cid); err != nil {
		fmt.Printf("REMOVE FAILED: %v\n", err)
	}
	sample(ctx, mgr, cid, "dns removed")
}

func testConnDrop(ctx context.Context, mgr *sidecar.Manager, cid string) {
	fw := firewall.New(mgr)
	fmt.Println("\n========== CONN DROP (30303 tcp DROP) ==========")
	if err := fw.InjectConnectionDrop(ctx, cid, firewall.ConnectionDropParams{
		RuleType:    "drop",
		TargetPorts: "30303",
		TargetProto: "tcp",
	}); err != nil {
		fmt.Printf("INJECT FAILED: %v\n", err)
		return
	}
	sample(ctx, mgr, cid, "conndrop injected")

	if err := fw.RemoveFault(ctx, cid); err != nil {
		fmt.Printf("REMOVE FAILED: %v\n", err)
	}
	sample(ctx, mgr, cid, "conndrop removed")
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: smoke-network <containerID> <netem|dns|conndrop|all>")
		os.Exit(2)
	}
	cid := os.Args[1]
	mode := os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli, err := docker.New()
	if err != nil {
		fmt.Printf("new docker client: %v\n", err)
		os.Exit(1)
	}
	mgr := sidecar.New(cli, sidecarImage)

	if _, err := mgr.CreateSidecar(ctx, cid); err != nil {
		fmt.Printf("create sidecar: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		fmt.Println("\n--- tearing down sidecar ---")
		if err := mgr.DestroySidecar(ctx, cid); err != nil {
			fmt.Printf("destroy sidecar: %v\n", err)
		}
	}()

	sample(ctx, mgr, cid, "baseline")

	switch mode {
	case "netem":
		testNetem(ctx, mgr, cid)
	case "dns":
		testDNS(ctx, mgr, cid)
	case "conndrop":
		testConnDrop(ctx, mgr, cid)
	case "all":
		testNetem(ctx, mgr, cid)
		testDNS(ctx, mgr, cid)
		testConnDrop(ctx, mgr, cid)
	default:
		fmt.Printf("unknown mode: %s\n", mode)
		os.Exit(2)
	}
}
