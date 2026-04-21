// Smoke harness for disk.FillWrapper + disk.FileOpsWrapper against a live
// container. Restricts itself to /tmp (or a provided path) to avoid touching
// bor's real chaindata.
//
// Usage:
//
//	go run ./cmd/smoke-disk <containerID> <mode> [extra args]
//
// modes:
//
//	fill <target-dir> <sizeMB>       — disk_fill test
//	delete <target-file>             — file_delete (creates + deletes)
//	corrupt <target-file>            — file_corrupt (creates + corrupts + restores)
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jihwankim/chaos-utils/pkg/discovery/docker"
	"github.com/jihwankim/chaos-utils/pkg/injection/disk"
)

func execOut(ctx context.Context, cli *docker.Client, cid string, cmd []string) string {
	out, err := cli.ExecCommand(ctx, cid, cmd)
	if err != nil {
		return fmt.Sprintf("[err: %v] %s", err, out)
	}
	return out
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: smoke-disk <containerID> <fill|delete|corrupt> [...]")
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

	switch mode {
	case "fill":
		if len(os.Args) < 5 {
			fmt.Println("usage: smoke-disk <cid> fill <dir> <sizeMB>")
			os.Exit(2)
		}
		dir := os.Args[3]
		var sizeMB int
		fmt.Sscanf(os.Args[4], "%d", &sizeMB)

		fw := disk.NewFillWrapper(cli)
		fmt.Printf("=== pre: df %s ===\n", dir)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("df -h %s", dir)}))

		fmt.Println("=== INJECT ===")
		if err := fw.InjectDiskFill(ctx, cid, disk.FillParams{
			TargetPath: dir, SizeMB: sizeMB, FileName: "chaos_smoke_fill",
		}); err != nil {
			fmt.Printf("INJECT FAILED: %v\n", err)
			return
		}
		fmt.Printf("=== post-inject: ls + df %s ===\n", dir)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("ls -la %s/chaos_smoke_fill 2>&1; df -h %s", dir, dir)}))

		fmt.Println("=== REMOVE ===")
		if err := fw.RemoveFault(ctx, cid); err != nil {
			fmt.Printf("REMOVE FAILED: %v\n", err)
			return
		}
		fmt.Printf("=== post-remove: ls + df %s ===\n", dir)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("ls -la %s/chaos_smoke_fill 2>&1; df -h %s", dir, dir)}))

	case "delete":
		if len(os.Args) < 4 {
			fmt.Println("usage: smoke-disk <cid> delete <file>")
			os.Exit(2)
		}
		path := os.Args[3]
		fops := disk.NewFileOpsWrapper(cli)
		fmt.Println("=== setup: create target file ===")
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("echo 'smoke content' > %s; ls -la %s", path, path)}))

		fmt.Println("=== INJECT delete ===")
		if err := fops.InjectFileDelete(ctx, cid, disk.FileDeleteParams{
			TargetPath: path, BackupFirst: true,
		}); err != nil {
			fmt.Printf("INJECT FAILED: %v\n", err)
			return
		}
		fmt.Printf("=== post-inject: stat %s ===\n", path)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("ls -la %s 2>&1; ls -la %s.chaos_backup 2>&1 || true", path, path)}))

		fmt.Println("=== REMOVE (restore backup) ===")
		if err := fops.RestoreAllBackups(ctx, cid); err != nil {
			fmt.Printf("RESTORE FAILED: %v\n", err)
			return
		}
		fmt.Printf("=== post-remove: ls %s ===\n", path)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("ls -la %s 2>&1; cat %s 2>&1", path, path)}))

	case "corrupt":
		if len(os.Args) < 4 {
			fmt.Println("usage: smoke-disk <cid> corrupt <file>")
			os.Exit(2)
		}
		path := os.Args[3]
		fops := disk.NewFileOpsWrapper(cli)
		fmt.Println("=== setup: create target file (256 bytes of 'A') ===")
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("yes A | head -c 256 > %s; md5sum %s", path, path)}))

		fmt.Println("=== INJECT corrupt (zero 16 bytes at offset 0) ===")
		if err := fops.InjectFileCorrupt(ctx, cid, disk.FileCorruptParams{
			TargetPath: path, Method: "zero", CorruptBytes: 16, CorruptOffset: 1, BackupFirst: true,
		}); err != nil {
			fmt.Printf("INJECT FAILED: %v\n", err)
			return
		}
		fmt.Printf("=== post-inject: md5 + xxd -l 32 %s ===\n", path)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("md5sum %s; xxd -l 32 %s 2>/dev/null || od -c -N 32 %s", path, path, path)}))

		fmt.Println("=== REMOVE (restore backup) ===")
		if err := fops.RestoreAllBackups(ctx, cid); err != nil {
			fmt.Printf("RESTORE FAILED: %v\n", err)
			return
		}
		fmt.Printf("=== post-remove: md5 %s ===\n", path)
		fmt.Print(execOut(ctx, cli, cid, []string{"sh", "-c", fmt.Sprintf("md5sum %s", path)}))

	default:
		fmt.Printf("unknown mode: %s\n", mode)
		os.Exit(2)
	}
}
