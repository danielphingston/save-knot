//go:build !windows

package registrybackup

import "context"

func backup(context.Context, []string) ([]byte, error) {
	return nil, ErrUnsupported
}

func restore(context.Context, []byte) error {
	return ErrUnsupported
}
