//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func ConfigureAutostart(enabled bool) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("locate autostart directory: %w", err)
	}
	path := filepath.Join(configDir, "autostart", "saveknot.desktop")
	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("disable SaveKnot launch at login: %w", err)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate SaveKnot executable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create autostart directory: %w", err)
	}
	content := "[Desktop Entry]\nType=Application\nName=SaveKnot\nExec=" + strconv.Quote(executable) + "\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("enable SaveKnot launch at login: %w", err)
	}
	return nil
}
