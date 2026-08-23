//go:build !windows

package discovery

func platformSteamRoots() []string {
	return nil
}
