package database

import (
	"context"
	"time"
)

func relocationCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

func (r *BlobRelocation) cleanupDestinationsAfterFailure(ctx context.Context) error {
	cleanupCtx, cancel := relocationCompensationContext(ctx)
	defer cancel()
	return r.cleanupDestinations(cleanupCtx)
}
