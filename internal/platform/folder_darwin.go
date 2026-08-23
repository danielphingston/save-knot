//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func SelectFolder(ctx context.Context) (string, error) {
	//nolint:gosec // The AppleScript is constant; no request data reaches the shell.
	output, err := exec.CommandContext(ctx, "osascript", "-e", `POSIX path of (choose folder with prompt "Choose a folder for SaveKnot")`).Output()
	if err != nil {
		return "", fmt.Errorf("open macOS folder picker: %w", err)
	}
	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", ErrCanceled
	}
	return selected, nil
}
