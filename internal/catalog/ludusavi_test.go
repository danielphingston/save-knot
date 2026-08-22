package catalog

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testManifest = `Example Game:
  files:
    "<home>/Example/*.sav":
      tags: [save]
  registry:
    HKEY_CURRENT_USER/Software/Example:
      tags: [save]
  steam:
    id: 1234
`

func TestParseIndexesSteamGames(t *testing.T) {
	t.Parallel()
	manifest, err := Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	game, ok := manifest.SteamGame("1234")
	if !ok || game.Name != "Example Game" || len(game.Files) != 1 || len(game.Registry) != 1 {
		t.Fatalf("unexpected game definition: %#v, found=%v", game, ok)
	}
}

func TestFetcherUsesETagAndKeepsValidatedCache(t *testing.T) {
	t.Parallel()
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("If-None-Match") == `"catalog-v1"` {
			return response(http.StatusNotModified, "", nil), nil
		}
		result := response(http.StatusOK, testManifest, nil)
		result.Header.Set("ETag", `"catalog-v1"`)
		return result, nil
	})}
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest.yaml")
	etagPath := filepath.Join(directory, "manifest.etag")
	fetcher := Fetcher{Client: client, URL: "https://catalog.invalid/manifest.yaml", Path: manifestPath, ETagPath: etagPath}
	updated, err := fetcher.Update(context.Background())
	if err != nil || !updated {
		t.Fatalf("first update: updated=%v, err=%v", updated, err)
	}
	updated, err = fetcher.Update(context.Background())
	if err != nil || updated {
		t.Fatalf("second update: updated=%v, err=%v", updated, err)
	}
	if requests != 2 {
		t.Fatalf("expected two requests, got %d", requests)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != testManifest {
		t.Fatal("cached manifest changed")
	}
}

func TestFetcherRejectsInvalidManifest(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, ":", nil), nil
	})}
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	_, err := (Fetcher{Client: client, URL: "https://catalog.invalid/manifest.yaml", Path: path, ETagPath: path + ".etag"}).Update(context.Background())
	if err == nil {
		t.Fatal("expected malformed manifest to be rejected")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("invalid manifest was cached: %v", statErr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
