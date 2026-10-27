//go:build windows

package discovery

import "golang.org/x/sys/windows/registry"

func platformUbisoftRoots() []string {
	var roots []string
	for _, view := range []uint32{registry.WOW64_32KEY, registry.WOW64_64KEY} {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Ubisoft\Launcher`, registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		value, _, valueErr := key.GetStringValue("InstallDir")
		closeErr := key.Close()
		if valueErr == nil && closeErr == nil && value != "" {
			roots = append(roots, value)
		}
	}
	return roots
}
