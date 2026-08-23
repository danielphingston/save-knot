//go:build windows

package discovery

import "golang.org/x/sys/windows/registry"

func platformSteamRoots() []string {
	locations := []struct {
		root registry.Key
		path string
		name string
		view uint32
	}{
		{root: registry.CURRENT_USER, path: `Software\Valve\Steam`, name: "SteamPath"},
		{root: registry.LOCAL_MACHINE, path: `Software\Valve\Steam`, name: "InstallPath", view: registry.WOW64_32KEY},
		{root: registry.LOCAL_MACHINE, path: `Software\Valve\Steam`, name: "InstallPath", view: registry.WOW64_64KEY},
	}
	var roots []string
	for _, location := range locations {
		key, err := registry.OpenKey(location.root, location.path, registry.QUERY_VALUE|location.view)
		if err != nil {
			continue
		}
		value, _, valueErr := key.GetStringValue(location.name)
		closeErr := key.Close()
		if valueErr == nil && closeErr == nil && value != "" {
			roots = append(roots, value)
		}
	}
	return roots
}
