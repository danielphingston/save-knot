//go:build windows

package database

import "golang.org/x/sys/windows"

func renameNoReplace(source, destination string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	// MoveFile fails when the destination exists, unlike MoveFileEx with REPLACE_EXISTING.
	return windows.MoveFile(sourcePath, destinationPath)
}
