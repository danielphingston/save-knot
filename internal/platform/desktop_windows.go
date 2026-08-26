//go:build windows

package platform

import (
	"context"
	"fmt"
	"log/slog"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

func RunDesktop(ctx context.Context, webpage string, run func(context.Context) error, cancel context.CancelFunc) error {
	runErrors := make(chan error, 1)
	go func() {
		runErrors <- run(ctx)
		// Let the initialized tray loop observe cancellation and close itself.
		// Calling Quit before Windows creates the tray window can lose the close
		// message when the HTTP server fails immediately (for example, port busy).
		cancel()
	}()

	systray.Run(func() {
		systray.SetIcon(saveKnotTrayIcon())
		systray.SetTooltip("SaveKnot is running")
		openItem := systray.AddMenuItem("Open SaveKnot", "Open the local SaveKnot webpage")
		systray.AddSeparator()
		exitItem := systray.AddMenuItem("Exit", "Stop SaveKnot and exit")

		go func() {
			for {
				select {
				case <-openItem.ClickedCh:
					if err := openWebpage(webpage); err != nil {
						slog.Error("open SaveKnot webpage", "url", webpage, "error", err)
					}
				case <-exitItem.ClickedCh:
					exitItem.SetTitle("Exiting…")
					exitItem.Disable()
					cancel()
				case <-ctx.Done():
					systray.Quit()
					return
				}
			}
		}()
	}, cancel)

	return <-runErrors
}

func openWebpage(webpage string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return fmt.Errorf("encode browser action: %w", err)
	}
	target, err := windows.UTF16PtrFromString(webpage)
	if err != nil {
		return fmt.Errorf("encode webpage URL: %w", err)
	}
	if err := windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("launch default browser: %w", err)
	}
	return nil
}
