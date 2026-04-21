// Smoke-test harness for disk.IODelayWrapper. Used during the April 2026
// fault-injection audit to prove the inject/verify/remove cycle works against
// a live kurtosis-pos container. Retained as a template for similar smoke
// harnesses against other fault wrappers.
//
// Usage: go run ./cmd/smoke-iodelay <containerID>
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/disk"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: smoke-iodelay <containerID>")
		os.Exit(2)
	}
	cid := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := docker.New()
	if err != nil {
		fmt.Printf("new client: %v\n", err)
		os.Exit(1)
	}

	iw := disk.New(cli)
	params := disk.IODelayParams{
		IOLatencyMs: 200,
		TargetPath:  "/var/lib/bor/bor/chaindata",
		Operation:   "write",
	}

	fmt.Println("=== INJECT ===")
	if err := iw.InjectIODelay(ctx, cid, params); err != nil {
		fmt.Printf("INJECT FAILED: %v\n", err)
		os.Exit(1)
	}

	time.Sleep(3 * time.Second)
	fmt.Println("\n=== RUNTIME SAMPLE ===")
	out, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c",
		`echo '-- pidfile --'; cat /var/lib/bor/bor/chaindata/.chaos_io_stress.pids 2>/dev/null || echo '<missing>';
		echo '-- kill -0 on each pid --'; while read p; do [ -z "$p" ] && continue; kill -0 "$p" 2>/dev/null && echo "$p ALIVE" || echo "$p DEAD"; done < /var/lib/bor/bor/chaindata/.chaos_io_stress.pids 2>/dev/null;
		echo '-- dd processes --'; ps -ef | grep -E '(^|[ \t])dd ' | grep -v grep || true;
		echo '-- stress files --'; ls -la /var/lib/bor/bor/chaindata/.chaos_io_stress* 2>/dev/null || true`})
	fmt.Println(out)

	fmt.Println("\n=== REMOVE ===")
	if err := iw.RemoveFault(ctx, cid, params); err != nil {
		fmt.Printf("REMOVE FAILED: %v\n", err)
		os.Exit(1)
	}

	time.Sleep(1 * time.Second)
	fmt.Println("\n=== POST-REMOVE SAMPLE ===")
	out2, _ := cli.ExecCommand(ctx, cid, []string{"sh", "-c",
		`echo '-- residual chaos_io_stress processes --';
		MY=$$; COUNT=0;
		for p in /proc/[0-9]*/cmdline; do
		  PID=$(basename "$(dirname "$p")");
		  [ "$PID" = "$MY" ] && continue;
		  CMD=$({ tr '\0' ' ' < "$p"; } 2>/dev/null);
		  case "$CMD" in *chaos_io_stress*) COUNT=$((COUNT+1)); echo "LEAK pid=$PID cmd=$CMD";; esac;
		done;
		echo "leak_count=$COUNT";
		echo '-- stress files remaining --'; ls -la /var/lib/bor/bor/chaindata/.chaos_io_stress* 2>/dev/null || echo '<none>'`})
	fmt.Println(out2)
}
