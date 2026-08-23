//go:build !windows && !darwin

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func SelectFolder(ctx context.Context) (string, error) {
	commands := [][]string{
		{"zenity", "--file-selection", "--directory", "--title=Choose a folder for SaveKnot"},
		{"kdialog", "--getexistingdirectory", ".", "--title", "Choose a folder for SaveKnot"},
	}
	var failures []error
	for _, arguments := range commands {
		//nolint:gosec // Every executable and argument is an application constant.
		output, err := exec.CommandContext(ctx, arguments[0], arguments[1:]...).Output()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		selected := strings.TrimSpace(string(output))
		if selected == "" {
			return "", ErrCanceled
		}
		return selected, nil
	}
	return "", fmt.Errorf("open folder picker (install zenity or kdialog): %w", errors.Join(failures...))
}
