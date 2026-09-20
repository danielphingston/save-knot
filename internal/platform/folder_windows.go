//go:build windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func SelectFolder(ctx context.Context) (string, error) {
	// Give the folder dialog a real, centered owner. An ownerless dialog launched
	// by a hidden PowerShell process can remain behind the browser or appear
	// minimized in the taskbar.
	const script = `
Add-Type -AssemblyName System.Windows.Forms

$owner = New-Object System.Windows.Forms.Form
$owner.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterScreen
$owner.ShowInTaskbar = $false
$owner.TopMost = $true
$owner.Opacity = 0

$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Choose a folder for SaveKnot'

try {
    $owner.Add_Shown({
        $owner.Activate()
        if ($dialog.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
            [Console]::Out.Write($dialog.SelectedPath)
        }
        $owner.Close()
    })
    [void]$owner.ShowDialog()
} finally {
    $dialog.Dispose()
    $owner.Dispose()
}`
	//nolint:gosec // The command and script are constants; no request data reaches the shell.
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("open Windows folder picker: %w", err)
	}
	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", ErrCanceled
	}
	return selected, nil
}
