//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func ConfigureAutostart(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open per-user Windows startup registry key: %w", err)
	}
	if !enabled {
		deleteErr := key.DeleteValue("SaveKnot")
		if errors.Is(deleteErr, registry.ErrNotExist) {
			deleteErr = nil
		}
		if err := errors.Join(deleteErr, key.Close()); err != nil {
			return fmt.Errorf("disable SaveKnot launch at login: %w", err)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate SaveKnot executable: %w", err)
	}
	command := `"` + strings.ReplaceAll(executable, `"`, `\"`) + `"`
	if err := errors.Join(key.SetStringValue("SaveKnot", command), key.Close()); err != nil {
		return fmt.Errorf("enable SaveKnot launch at login: %w", err)
	}
	return nil
}
