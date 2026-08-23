//go:build darwin

package platform

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
)

func ConfigureAutostart(enabled bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locate LaunchAgents directory: %w", err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "com.saveknot.agent.plist")
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
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>com.saveknot.agent</string><key>ProgramArguments</key><array><string>` + html.EscapeString(executable) + `</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("enable SaveKnot launch at login: %w", err)
	}
	return nil
}
