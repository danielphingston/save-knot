//go:build !linux && !darwin && !windows

package database

import "errors"

var errNoReplaceRenameUnsupported = errors.New("atomic no-replace rename is unsupported on this platform")

func renameNoReplace(_, _ string) error {
	return errNoReplaceRenameUnsupported
}
