package registrybackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestRestoreReconcilesValuesAndSubkeysAfterFailedTarget(t *testing.T) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	path := `Software\SaveKnot\RegistryBackupTests\` + hex.EncodeToString(suffix[:])
	fullPath := `HKCU\` + path
	outsidePath := path + "-outside"
	t.Cleanup(func() {
		if err := deleteTestTree(path); err != nil {
			t.Errorf("clean up registry test key: %v", err)
		}
		if err := deleteTestTree(outsidePath); err != nil {
			t.Errorf("clean up outside registry test key: %v", err)
		}
	})

	outsideKey, _, err := registry.CreateKey(registry.CURRENT_USER, outsidePath, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := outsideKey.SetStringValue("outside", "preserve"); err != nil {
		t.Fatal(errors.Join(err, outsideKey.Close()))
	}
	if err := outsideKey.Close(); err != nil {
		t.Fatal(err)
	}

	baseline := "original"
	prior := Document{Version: 1, Keys: []KeyData{{
		Path:   fullPath,
		Values: []ValueData{{Name: "keep", Type: registry.SZ, String: &baseline}},
	}}}
	if err := restoreTestDocument(prior, []string{fullPath}); err != nil {
		t.Fatal(err)
	}
	changed := "changed"
	introduced := "new"
	target := Document{Version: 1, Keys: []KeyData{
		{Path: fullPath, Values: []ValueData{
			{Name: "keep", Type: registry.SZ, String: &changed},
			{Name: "introduced", Type: registry.SZ, String: &introduced},
		}},
		{Path: fullPath + `\introduced-child`, Values: []ValueData{
			{Name: "nested", Type: registry.SZ, String: &introduced},
		}},
	}}
	if err := restoreTestDocument(target, []string{fullPath}); err != nil {
		t.Fatalf("restore target registry state: %v", err)
	}
	if err := restoreTestDocument(prior, []string{fullPath}); err != nil {
		t.Fatalf("restore prior registry state: %v", err)
	}

	failedTarget := Document{Version: 1, Keys: append(target.Keys, KeyData{
		Path: fullPath + `\failed-child`, Values: []ValueData{{Name: "invalid", Type: registry.SZ}},
	})}
	if err := restoreTestDocument(failedTarget, []string{fullPath}); err == nil {
		t.Fatal("target restore unexpectedly succeeded with an invalid registry value")
	}

	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	value, _, err := key.GetStringValue("keep")
	if err != nil || value != baseline {
		t.Fatalf("prior value was not restored: got %q, err=%v", value, err)
	}
	if _, _, err := key.GetStringValue("introduced"); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("target-only value remains after rollback: %v", err)
	}
	for _, child := range []string{"introduced-child", "failed-child"} {
		if _, err := registry.OpenKey(registry.CURRENT_USER, path+`\`+child, registry.READ); !errors.Is(err, registry.ErrNotExist) {
			t.Fatalf("target-only key %q remains after rollback: %v", child, err)
		}
	}
	outsideKey, err = registry.OpenKey(registry.CURRENT_USER, outsidePath, registry.READ)
	if err != nil {
		t.Fatalf("configured root reconciliation changed an outside key: %v", err)
	}
	defer outsideKey.Close()
	if outside, _, err := outsideKey.GetStringValue("outside"); err != nil || outside != "preserve" {
		t.Fatalf("outside key was changed: got %q, err=%v", outside, err)
	}
	outsideSnapshot := Document{Version: 1, Keys: []KeyData{{Path: `HKCU\` + outsidePath}}}
	if err := restoreTestDocument(outsideSnapshot, []string{fullPath}); err == nil {
		t.Fatal("restore accepted a document path outside its configured root")
	}
}

func restoreTestDocument(document Document, roots []string) error {
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	return (Service{}).Restore(context.Background(), data, roots)
}

func deleteTestTree(path string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	children, readErr := key.ReadSubKeyNames(-1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, key.Close()); err != nil {
		return err
	}
	for _, child := range children {
		if err := deleteTestTree(path + `\` + child); err != nil {
			return err
		}
	}
	return registry.DeleteKey(registry.CURRENT_USER, strings.ReplaceAll(path, "/", `\`))
}
