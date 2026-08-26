//go:build !windows

package platform

import "context"

func RunDesktop(ctx context.Context, _ string, run func(context.Context) error, _ context.CancelFunc) error {
	return run(ctx)
}
