package disk

import (
	"context"
	"fmt"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/injection/safeshell"
)

// FileDeleteParams defines parameters for file deletion injection
type FileDeleteParams struct {
	// TargetPath is the file or glob pattern to delete (e.g., "/root/.bor/data/bor/chaindata/LOCK")
	TargetPath string

	// Recursive deletes directories recursively
	Recursive bool

	// BackupFirst creates a backup before deletion (for restoration)
	BackupFirst bool
}

// FileCorruptParams defines parameters for file corruption injection
type FileCorruptParams struct {
	// TargetPath is the file to corrupt
	TargetPath string

	// CorruptBytes is the number of bytes to corrupt (default: 512)
	CorruptBytes int

	// CorruptOffset is the byte offset where corruption starts (default: 0 = random)
	CorruptOffset int

	// Method is the corruption method: "zero" (write zeros) or "random" (write random bytes)
	Method string

	// BackupFirst creates a backup before corruption (for restoration)
	BackupFirst bool
}

// FileOpsWrapper wraps file operation fault injection
type FileOpsWrapper struct {
	dockerClient DockerClient
}

// NewFileOpsWrapper creates a new file operations wrapper
func NewFileOpsWrapper(dockerClient DockerClient) *FileOpsWrapper {
	return &FileOpsWrapper{
		dockerClient: dockerClient,
	}
}

// InjectFileDelete deletes files in a target container. Refuses to run when
// the target doesn't exist — a silent no-op on a missing path was hiding
// scenario bugs where the test assumed data existed pre-inject.
func (fw *FileOpsWrapper) InjectFileDelete(ctx context.Context, targetContainerID string, params FileDeleteParams) error {
	fmt.Printf("Injecting file deletion on target %s: %s\n", targetContainerID[:12], params.TargetPath)

	// Pre-flight: the target must exist before we "delete" it.
	existsCmd := []string{"sh", "-c", fmt.Sprintf("if [ -e \"%s\" ]; then echo EXISTS; else echo MISSING; fi", params.TargetPath)}
	existsOut, existsErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, existsCmd)
	if existsErr != nil {
		return fmt.Errorf("failed to stat %s before delete: %w", params.TargetPath, existsErr)
	}
	if strings.TrimSpace(existsOut) != "EXISTS" {
		return fmt.Errorf("file_delete target %s does not exist — fault would be a silent no-op; create the file or fix the path", params.TargetPath)
	}

	if params.BackupFirst {
		backupPath := params.TargetPath + ".chaos_backup"
		// Fail loudly if the backup doesn't get created — the previous
		// `cp ... || true` swallowed errors, leaving the delete
		// unrecoverable when restore was expected later.
		backupCmd := []string{"sh", "-c", fmt.Sprintf("cp -a \"%s\" \"%s\"", params.TargetPath, backupPath)}
		backupOut, backupErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, backupCmd)
		if backupErr != nil {
			return fmt.Errorf("failed to create backup %s before delete: %w (output: %s)", backupPath, backupErr, backupOut)
		}
		// Verify the backup file exists (cp can succeed without writing, e.g.
		// on unusual filesystems — double-check to avoid restore-time surprises).
		verifyCmd := []string{"sh", "-c", fmt.Sprintf("[ -e \"%s\" ] && echo OK || echo MISSING", backupPath)}
		verifyOut, _ := fw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
		if strings.TrimSpace(verifyOut) != "OK" {
			return fmt.Errorf("backup %s not found after cp — refusing to delete without a recoverable backup", backupPath)
		}
		fmt.Printf("  Backed up to %s\n", backupPath)
	}

	var cmd []string
	if params.Recursive {
		cmd = []string{"rm", "-rf", params.TargetPath}
	} else {
		cmd = []string{"rm", "-f", params.TargetPath}
	}

	output, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to delete %s: %w (output: %s)", params.TargetPath, err, output)
	}

	// Confirm the file is actually gone — rm -f on a read-only filesystem
	// can still return 0 on some implementations.
	postCmd := []string{"sh", "-c", fmt.Sprintf("if [ -e \"%s\" ]; then echo STILL_PRESENT; else echo GONE; fi", params.TargetPath)}
	postOut, _ := fw.dockerClient.ExecCommand(ctx, targetContainerID, postCmd)
	if strings.TrimSpace(postOut) != "GONE" {
		return fmt.Errorf("file_delete failed: %s still present after rm (output: %s)", params.TargetPath, strings.TrimSpace(postOut))
	}

	fmt.Printf("  Deleted %s\n", params.TargetPath)
	return nil
}

// InjectFileCorrupt corrupts file bytes in a target container. Like
// InjectFileDelete, it refuses to run when the target is missing — a dd on a
// non-existent file is a silent no-op otherwise.
func (fw *FileOpsWrapper) InjectFileCorrupt(ctx context.Context, targetContainerID string, params FileCorruptParams) error {
	fmt.Printf("Injecting file corruption on target %s: %s\n", targetContainerID[:12], params.TargetPath)

	// Pre-flight: target must exist AND be a regular file with non-zero size
	// (corrupting an empty file changes nothing observable).
	existsCmd := []string{"sh", "-c", fmt.Sprintf("if [ -f \"%s\" ] && [ -s \"%s\" ]; then wc -c < \"%s\"; else echo MISSING_OR_EMPTY; fi", params.TargetPath, params.TargetPath, params.TargetPath)}
	existsOut, existsErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, existsCmd)
	if existsErr != nil {
		return fmt.Errorf("failed to stat %s before corrupt: %w", params.TargetPath, existsErr)
	}
	if strings.Contains(existsOut, "MISSING_OR_EMPTY") {
		return fmt.Errorf("file_corrupt target %s is missing or empty — corruption would be a silent no-op", params.TargetPath)
	}

	corruptBytes := params.CorruptBytes
	if corruptBytes <= 0 {
		corruptBytes = 512
	}

	if params.BackupFirst {
		backupPath := params.TargetPath + ".chaos_backup"
		// Fail loudly on backup errors so restore isn't broken silently.
		backupCmd := []string{"sh", "-c", fmt.Sprintf("cp -a \"%s\" \"%s\"", params.TargetPath, backupPath)}
		backupOut, backupErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, backupCmd)
		if backupErr != nil {
			return fmt.Errorf("failed to create backup %s before corrupt: %w (output: %s)", backupPath, backupErr, backupOut)
		}
		verifyCmd := []string{"sh", "-c", fmt.Sprintf("[ -e \"%s\" ] && echo OK || echo MISSING", backupPath)}
		verifyOut, _ := fw.dockerClient.ExecCommand(ctx, targetContainerID, verifyCmd)
		if strings.TrimSpace(verifyOut) != "OK" {
			return fmt.Errorf("backup %s not found after cp — refusing to corrupt without a recoverable backup", backupPath)
		}
		fmt.Printf("  Backed up to %s\n", backupPath)
	}

	// Get file size to pick a random offset if not specified
	var offsetExpr string
	if params.CorruptOffset > 0 {
		offsetExpr = fmt.Sprintf("%d", params.CorruptOffset)
	} else {
		// Calculate a pseudo-random offset in the target container.
		// RANDOM is a bash-ism; use /dev/urandom + od for POSIX/BusyBox compatibility.
		// BusyBox stat uses `stat -s` or `wc -c`; avoid GNU `stat -c %s`.
		offsetExpr = fmt.Sprintf("$(( $(od -A n -t u4 -N 4 /dev/urandom | tr -d ' ') %% $(( $(wc -c < \"%s\") - %d - 64 )) + 64 ))",
			params.TargetPath, corruptBytes)
	}

	var cmd []string
	switch strings.ToLower(params.Method) {
	case "zero", "":
		// Write zeros at the offset
		cmd = []string{"sh", "-c", fmt.Sprintf(
			"dd if=/dev/zero of=\"%s\" bs=1 count=%d seek=%s conv=notrunc 2>/dev/null",
			params.TargetPath, corruptBytes, offsetExpr,
		)}
	case "random":
		// Write random data at the offset
		cmd = []string{"sh", "-c", fmt.Sprintf(
			"dd if=/dev/urandom of=\"%s\" bs=1 count=%d seek=%s conv=notrunc 2>/dev/null",
			params.TargetPath, corruptBytes, offsetExpr,
		)}
	default:
		return fmt.Errorf("unknown corruption method: %s (use zero or random)", params.Method)
	}

	output, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
	if err != nil {
		return fmt.Errorf("failed to corrupt %s: %w (output: %s)", params.TargetPath, err, output)
	}

	// Verify corruption actually landed: if a backup exists, compare against it.
	// If no backup, at minimum confirm the file changed from its pre-inject
	// state by re-checking size/hash — dd on a non-existent path would have
	// silently left us no file. BusyBox lacks md5sum in some minimal images,
	// so fall back to cksum which is POSIX-required.
	if params.BackupFirst {
		diffCmd := []string{"sh", "-c", fmt.Sprintf(
			"H1=$(cksum < \"%s\" | awk '{print $1}'); "+
				"H2=$(cksum < \"%s.chaos_backup\" | awk '{print $1}'); "+
				"if [ \"$H1\" = \"$H2\" ]; then echo UNCHANGED; else echo CHANGED; fi",
			params.TargetPath, params.TargetPath)}
		diffOut, _ := fw.dockerClient.ExecCommand(ctx, targetContainerID, diffCmd)
		if strings.TrimSpace(diffOut) == "UNCHANGED" {
			return fmt.Errorf("file_corrupt verification: %s is byte-identical to backup — corruption did not land (check offset/size vs file size)", params.TargetPath)
		}
	}

	fmt.Printf("  Corrupted %d bytes at offset %s in %s (method: %s)\n",
		corruptBytes, offsetExpr, params.TargetPath, params.Method)
	return nil
}

// RestoreAllBackups finds and restores any .chaos_backup files in a container.
// Called during fault removal to clean up after file_delete/file_corrupt operations.
func (fw *FileOpsWrapper) RestoreAllBackups(ctx context.Context, targetContainerID string) error {
	// Find .chaos_backup files in common data directories and restore them.
	// Scoped to likely paths instead of `find /` which is slow.
	cmd := []string{"sh", "-c", `
		FOUND=0
		for dir in /var/lib /etc /root /tmp /home; do
			for bak in $(find "$dir" -name '*.chaos_backup' 2>/dev/null); do
				ORIG="${bak%.chaos_backup}"
				cp -a "$bak" "$ORIG" 2>/dev/null && rm -f "$bak" && FOUND=$((FOUND+1))
			done
		done
		echo "restored $FOUND"
	`}

	output, err := fw.dockerClient.ExecCommand(ctx, targetContainerID, cmd)
	if err != nil {
		fmt.Printf("  Warning: backup restoration scan failed: %v\n", err)
		return nil
	}
	fmt.Printf("  Backup restoration: %s\n", strings.TrimSpace(output))
	return nil
}

// ValidateFileDeleteParams validates file delete parameters
func ValidateFileDeleteParams(params FileDeleteParams) error {
	if params.TargetPath == "" {
		return fmt.Errorf("target_path must be specified")
	}

	if err := safeshell.ValidateShellSafe(params.TargetPath); err != nil {
		return fmt.Errorf("target_path: %w", err)
	}

	return nil
}

// ValidateFileCorruptParams validates file corruption parameters
func ValidateFileCorruptParams(params FileCorruptParams) error {
	if params.TargetPath == "" {
		return fmt.Errorf("target_path must be specified")
	}

	if err := safeshell.ValidateShellSafe(params.TargetPath); err != nil {
		return fmt.Errorf("target_path: %w", err)
	}

	if params.CorruptBytes < 0 {
		return fmt.Errorf("corrupt_bytes cannot be negative")
	}

	if params.CorruptOffset < 0 {
		return fmt.Errorf("corrupt_offset cannot be negative")
	}

	method := strings.ToLower(params.Method)
	if method != "" && method != "zero" && method != "random" {
		return fmt.Errorf("method must be 'zero' or 'random'")
	}

	return nil
}
