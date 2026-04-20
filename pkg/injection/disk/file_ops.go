package disk

import (
	"context"
	"fmt"
	"strings"

	"github.com/jihwankim/chaos-utils/pkg/injection/safeshell"
	"github.com/rs/zerolog/log"
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

// InjectFileDelete deletes files in a target container
func (fw *FileOpsWrapper) InjectFileDelete(ctx context.Context, targetContainerID string, params FileDeleteParams) error {
	fmt.Printf("Injecting file deletion on target %s: %s\n", targetContainerID[:12], params.TargetPath)

	if params.BackupFirst {
		backupPath := params.TargetPath + ".chaos_backup"
		backupCmd := []string{"sh", "-c", fmt.Sprintf("cp -a \"%s\" \"%s\" 2>/dev/null || true", params.TargetPath, backupPath)}
		_, backupErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, backupCmd)
		if backupErr != nil {
			log.Warn().Err(backupErr).Str("container", targetContainerID[:12]).Str("path", backupPath).Msg("failed to create backup before delete")
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

	fmt.Printf("  Deleted %s\n", params.TargetPath)
	return nil
}

// InjectFileCorrupt corrupts file bytes in a target container
func (fw *FileOpsWrapper) InjectFileCorrupt(ctx context.Context, targetContainerID string, params FileCorruptParams) error {
	fmt.Printf("Injecting file corruption on target %s: %s\n", targetContainerID[:12], params.TargetPath)

	corruptBytes := params.CorruptBytes
	if corruptBytes <= 0 {
		corruptBytes = 512
	}

	if params.BackupFirst {
		backupPath := params.TargetPath + ".chaos_backup"
		backupCmd := []string{"sh", "-c", fmt.Sprintf("cp -a \"%s\" \"%s\" 2>/dev/null || true", params.TargetPath, backupPath)}
		_, backupErr := fw.dockerClient.ExecCommand(ctx, targetContainerID, backupCmd)
		if backupErr != nil {
			log.Warn().Err(backupErr).Str("container", targetContainerID[:12]).Str("path", backupPath).Msg("failed to create backup before corrupt")
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
