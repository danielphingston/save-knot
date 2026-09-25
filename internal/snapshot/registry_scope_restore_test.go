package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
	"github.com/saveknot/saveknot/internal/registrybackup"
)

func TestRestoreRejectsRegistryRootsOutsideEnabledLocalSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(trusted, other string) []string
	}{
		{name: "broader parent", target: func(trusted, _ string) []string {
			return []string{trusted[:strings.LastIndex(trusted, "\\")]}
		}},
		{name: "different root", target: func(_, other string) []string { return []string{other} }},
		{name: "extra disabled root", target: func(trusted, other string) []string { return []string{trusted, other} }},
		{name: "malformed traversal", target: func(trusted, _ string) []string { return []string{trusted + `\..\Other`} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, service, store, game, targetID, savePath, trusted, other := registryScopeRestoreFixture(t)
			target, err := service.repository.Snapshot(ctx, targetID)
			if err != nil {
				t.Fatal(err)
			}
			target.Registry.Keys = tc.target(trusted, other)
			// Import replaces the remote manifest, which is the untrusted input to Restore.
			if err := store.ImportSnapshot(ctx, target); err != nil {
				t.Fatal(err)
			}
			_, err = service.Restore(ctx, game, targetID)
			if err == nil || err.Error() != registryScopeMismatch {
				t.Fatalf("restore with untrusted registry roots = %v; want registry root scope error", err)
			}
			assertScopeSaveUnchanged(t, savePath)
		})
	}
}

func TestRestoreAllowsStrictSubsetOfEnabledRegistryRoots(t *testing.T) {
	ctx, service, _, game, targetID, _, trusted, _ := registryScopeRestoreFixture(t)
	target, err := service.repository.Snapshot(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := service.ValidateRestoreScope(ctx, game, target)
	if err != nil {
		t.Fatalf("strict subset of enabled roots rejected: %v", err)
	}
	want, err := registrybackup.NormalizeRootPaths([]string{trusted})
	if err != nil || len(roots) != 1 || len(want) != 1 || roots[0] != want[0] {
		t.Fatalf("validated subset roots = %v, want %v (normalization err %v)", roots, want, err)
	}
}

func TestRestoreAllowsCanonicalEquivalentRegistryRoots(t *testing.T) {
	ctx, service, store, game, targetID, savePath, trusted, _ := registryScopeRestoreFixture(t)
	target, err := service.repository.Snapshot(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	target.Registry.Keys = []string{strings.Replace(trusted, `HKCU`, `HKEY_CURRENT_USER`, 1) + `\`, trusted}
	if err := store.ImportSnapshot(ctx, target); err != nil {
		t.Fatal(err)
	}
	_, err = service.Restore(ctx, game, targetID)
	if runtime.GOOS != "windows" {
		if !errors.Is(err, registrybackup.ErrUnsupported) {
			t.Fatalf("canonical root was rejected before the unsupported registry backend: %v", err)
		}
		assertScopeSaveUnchanged(t, savePath)
		return
	}
	if err != nil {
		t.Fatalf("restore rejected canonical equivalent of enabled root: %v", err)
	}
	got, err := os.ReadFile(savePath)
	if err != nil || string(got) != "snapshot version" {
		t.Fatalf("trusted restore did not activate target save: data=%q err=%v", got, err)
	}
}

func TestRestorePreviousRegistryRejectsUntrustedRollbackRoots(t *testing.T) {
	ctx, service, _, _, targetID, savePath, _, other := registryScopeRestoreFixture(t)
	rollback, err := service.repository.Snapshot(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	rollback.Registry.Keys = []string{other}
	err = service.restorePreviousRegistry(ctx, rollback)
	if err == nil || !strings.Contains(err.Error(), registryScopeMismatch) {
		t.Fatalf("restore untrusted rollback registry roots = %v; want registry root scope error", err)
	}
	assertScopeSaveUnchanged(t, savePath)
}

func registryScopeRestoreFixture(t *testing.T) (context.Context, *Service, *database.Store, core.Game, string, string, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := database.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	savePath := filepath.Join(root, "slot.dat")
	if err := os.WriteFile(savePath, []byte("snapshot version"), 0o600); err != nil {
		t.Fatal(err)
	}
	game := core.Game{ID: "game", DisplayName: "Game", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPath(ctx, core.GamePath{
		ID: "path", GameID: game.ID, Source: "custom", Template: savePath, Resolved: savePath, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	service := New(store, store, filepath.Join(root, "blobs"), "device")
	target, err := service.Create(ctx, game)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(savePath, []byte("current version"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := core.NewID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	base := `HKCU\Software\SaveKnot\RestoreScopeTests\` + id
	trusted, other := base+`\Trusted`, base+`\Other`
	if err := store.ReplaceCatalogRegistry(ctx, game.ID, []core.RegistryPath{
		{ID: "trusted", GameID: game.ID, Source: "catalog", Path: trusted, Enabled: true},
		{ID: "trusted-extra", GameID: game.ID, Source: "catalog", Path: base + `\AlsoTrusted`, Enabled: true},
		{ID: "disabled", GameID: game.ID, Source: "catalog", Path: other, Enabled: false},
	}); err != nil {
		t.Fatal(err)
	}
	registryBlob, err := service.storeData([]byte(`{"version":1,"keys":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, registryBlob.hash, registryBlob.path, registryBlob.originalSize, registryBlob.storedSize); err != nil {
		t.Fatal(err)
	}
	target.ID = "remote-manifest"
	target.DeviceID = "another-device"
	target.Registry = &core.SnapshotRegistry{Keys: []string{trusted}, Hash: registryBlob.hash, Size: registryBlob.originalSize}
	if err := store.ImportSnapshot(ctx, target); err != nil {
		t.Fatal(err)
	}
	return ctx, service, store, game, target.ID, savePath, trusted, other
}

func assertScopeSaveUnchanged(t *testing.T, savePath string) {
	t.Helper()
	got, err := os.ReadFile(savePath)
	if err != nil || string(got) != "current version" {
		t.Fatalf("failed restore changed save: data=%q err=%v", got, err)
	}
}
