package app

import (
	"context"
	"fmt"

	"github.com/saveknot/saveknot/internal/database"
)

type localBlobRelocation interface {
	Switch(context.Context, bool) error
	Rollback(context.Context) error
	CleanupSources(context.Context) error
}

type legacyBlobRelocator interface {
	RelocateBlobs(context.Context, string) error
}

func (c *Coordinator) prepareLocalBlobRelocation(ctx context.Context, root string) (localBlobRelocation, legacyBlobRelocator, error) {
	if preparer, ok := c.repository.(interface {
		PrepareBlobRelocation(context.Context, string) (*database.BlobRelocation, error)
	}); ok {
		receipt, err := preparer.PrepareBlobRelocation(ctx, root)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare local backup relocation: %w", err)
		}
		return receipt, nil, nil
	}
	if preparer, ok := c.repository.(interface {
		PrepareLocalBlobRelocation(context.Context, string) (localBlobRelocation, error)
	}); ok {
		receipt, err := preparer.PrepareLocalBlobRelocation(ctx, root)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare local backup relocation: %w", err)
		}
		return receipt, nil, nil
	}
	relocator, ok := c.repository.(legacyBlobRelocator)
	if !ok {
		return nil, nil, nil
	}
	if err := relocator.RelocateBlobs(ctx, root); err != nil {
		return nil, nil, fmt.Errorf("move local backup blobs: %w", err)
	}
	return nil, relocator, nil
}
