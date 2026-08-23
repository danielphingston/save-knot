//go:build windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func SelectFolder(ctx context.Context) (string, error) {
	const script = `$folder = (New-Object -ComObject Shell.Application).BrowseForFolder(0, 'Choose a folder for SaveKnot', 0, 0); if ($null -ne $folder) { $folder.Self.Path }`
	//nolint:gosec // The command and script are constants; no request data reaches the shell.
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", fmt.Errorf("open Windows folder picker: %w", err)
	}
	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", ErrCanceled
	}
	return selected, nil
}
