// Smoke-test for stress.StressWrapper — audits CPU stress + memory limit
// against a live container. Used to investigate whether `sh -c "... & done"`
// in stress_wrapper.go:85 suffers from the same inherited-fd bug that was
// fixed in disk/io_delay.go.
//
// Usage: go run ./cmd/smoke-stress <containerID> <cpu|mem|mem-remove>
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/stress"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: smoke-stress <containerID> <cpu|mem|cpu-limit>")
		os.Exit(2)
	}
	cid, mode := os.Args[1], os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cli, err := docker.New()
	if err != nil {
		fmt.Printf("new client: %v\n", err)
		os.Exit(1)
	}
	sw := stress.New(cli)

	switch mode {
	case "cpu":
		// Active CPU stress — the one that uses `for i ...; yes > /dev/null & done`.
		params := stress.StressParams{Method: "stress", CPUPercent: 80, Cores: 2}
		t0 := time.Now()
		fmt.Println("=== INJECT CPU ===")
		if err := sw.InjectCPUStress(ctx, cid, params); err != nil {
			fmt.Printf("INJECT FAILED after %s: %v\n", time.Since(t0), err)
			os.Exit(1)
		}
		fmt.Printf("InjectCPUStress returned after %s\n", time.Since(t0))

		time.Sleep(5 * time.Second)
		fmt.Println("\n=== PROC COUNT (yes processes in container) ===")
		out, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c",
			"ps -ef | grep -E '^[^ ]+ +[0-9]+' | grep -E 'yes$|yes ' | grep -v grep | wc -l; echo '---'; ps aux 2>/dev/null | head -30"})
		fmt.Println(out)

		fmt.Println("\n=== REMOVE ===")
		t1 := time.Now()
		if err := sw.RemoveFault(ctx, cid); err != nil {
			fmt.Printf("REMOVE FAILED after %s: %v\n", time.Since(t1), err)
			os.Exit(1)
		}
		fmt.Printf("RemoveFault returned after %s\n", time.Since(t1))

		time.Sleep(2 * time.Second)
		out2, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c",
			"COUNT=0; for p in /proc/[0-9]*/cmdline; do tr '\\0' ' ' < $p 2>/dev/null | grep -q '^yes' && COUNT=$((COUNT+1)); done; echo leaked_yes=$COUNT"})
		fmt.Println(out2)

	case "mem":
		params := stress.StressParams{Method: "limit", MemoryMB: 2048}
		fmt.Println("=== INJECT MEM LIMIT ===")
		if err := sw.InjectMemoryStress(ctx, cid, params); err != nil {
			fmt.Printf("INJECT FAILED: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(1 * time.Second)
		out, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c", "cat /sys/fs/cgroup/memory.max 2>/dev/null || cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null || echo NOCGROUP"})
		fmt.Printf("Container cgroup memory.max = %s\n", out)

		fmt.Println("\n=== REMOVE ===")
		if err := sw.RemoveFault(ctx, cid); err != nil {
			fmt.Printf("REMOVE FAILED: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(1 * time.Second)
		out2, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c", "cat /sys/fs/cgroup/memory.max 2>/dev/null || cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null || echo NOCGROUP"})
		fmt.Printf("Post-remove cgroup memory.max = %s\n", out2)

	case "cpu-limit":
		params := stress.StressParams{Method: "limit", CPUPercent: 25, Cores: 1}
		fmt.Println("=== INJECT CPU LIMIT ===")
		if err := sw.InjectCPUStress(ctx, cid, params); err != nil {
			fmt.Printf("INJECT FAILED: %v\n", err)
			os.Exit(1)
		}
		out, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c", "cat /sys/fs/cgroup/cpu.max 2>/dev/null || cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us 2>/dev/null"})
		fmt.Printf("cpu.max = %s\n", out)

		fmt.Println("=== REMOVE ===")
		if err := sw.RemoveFault(ctx, cid); err != nil {
			fmt.Printf("REMOVE FAILED: %v\n", err)
			os.Exit(1)
		}
		out2, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c", "cat /sys/fs/cgroup/cpu.max 2>/dev/null || cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us 2>/dev/null"})
		fmt.Printf("post-remove cpu.max = %s\n", out2)

	default:
		fmt.Printf("unknown mode: %s\n", mode)
		os.Exit(2)
	}
}
