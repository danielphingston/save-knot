package registrybackup

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("windows registry backup is not supported on this operating system")

type Document struct {
	Version int       `json:"version"`
	Keys    []KeyData `json:"keys"`
}

type KeyData struct {
	Path   string      `json:"path"`
	Values []ValueData `json:"values,omitempty"`
}

type ValueData struct {
	Name    string   `json:"name"`
	Type    uint32   `json:"type"`
	String  *string  `json:"string,omitempty"`
	Strings []string `json:"strings,omitempty"`
	Integer *uint64  `json:"integer,omitempty"`
	Binary  []byte   `json:"binary,omitempty"`
}

type Service struct{}

func (Service) Backup(ctx context.Context, roots []string) ([]byte, error) {
	return backup(ctx, roots)
}

func (Service) Restore(ctx context.Context, data []byte) error {
	return restore(ctx, data)
}
