package remote

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
	"github.com/saveknot/saveknot/internal/snapshot"
)

// TestR2TwoDevicesEndToEnd runs against an S3 endpoint (the script starts
// MinIO), or against a disposable prefix in an actual R2 bucket. It uses two
// independent SQLite databases, blob directories, and save directories.
func TestR2TwoDevicesEndToEnd(t *testing.T) {
	ctx, r2 := newE2ER2(t)
	const gameID = "shared-catalog-game"
	const sourceTemplate = "<home>/Save Games/Shared Game"
	a := newE2EDevice(t, ctx, "computer-a", gameID)
	gameA := core.Game{ID: gameID, DisplayName: "Shared Game", Store: "steam", Enabled: true, SyncEnabled: true}
	first := e2eInitialBackup(t, ctx, r2, a, gameA, sourceTemplate)
	b := newE2EDevice(t, ctx, "computer-b", gameID)
	gameB := e2eImportAndRestore(t, ctx, r2, b, first, sourceTemplate)
	second := e2eReverseSync(t, ctx, r2, a, b, gameA, gameB)
	e2eRejectCorruptBlob(t, ctx, r2, gameID, sourceTemplate, second)
}

func newE2ER2(t *testing.T) (context.Context, *R2) {
	t.Helper()
	endpoint := os.Getenv("SAVEKNOT_E2E_S3_ENDPOINT")
	account := os.Getenv("SAVEKNOT_E2E_R2_ACCOUNT_ID")
	accessKey := os.Getenv("SAVEKNOT_E2E_R2_ACCESS_KEY_ID")
	secret := os.Getenv("SAVEKNOT_E2E_R2_SECRET_ACCESS_KEY")
	bucket := os.Getenv("SAVEKNOT_E2E_R2_BUCKET")
	if accessKey == "" || secret == "" || bucket == "" || (endpoint == "" && account == "") {
		t.Skip("set SAVEKNOT_E2E_S3_ENDPOINT (or R2 account ID), bucket, access key, and secret; or run scripts/test-r2-e2e.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	if account == "" {
		account = "local"
	}
	client, err := e2eS3Client(ctx, endpoint, account, accessKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "" {
		if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
			t.Fatalf("create test bucket: %v", err)
		}
	}
	prefix := fmt.Sprintf("saveknot-e2e-%d", time.Now().UnixNano())
	r2 := &R2{client: client, bucket: bucket, prefix: prefix, identity: account + "/" + bucket + "/" + prefix}
	t.Cleanup(func() { cleanupE2EObjects(t, client, bucket, prefix+"/v1/") })
	if err := r2.Test(ctx); err != nil {
		t.Fatalf("R2 capability test: %v", err)
	}
	return ctx, r2
}

func e2eInitialBackup(t *testing.T, ctx context.Context, r2 *R2, a *e2eDevice, gameA core.Game, sourceTemplate string) core.Snapshot {
	t.Helper()
	if err := a.store.UpsertGame(ctx, gameA); err != nil {
		t.Fatal(err)
	}
	a.addPath(t, ctx, sourceTemplate)
	a.writeSave(t, "A: first save")
	first, err := a.snapshots.Create(ctx, gameA)
	if err != nil {
		t.Fatalf("computer A backup: %v", err)
	}
	if _, err := a.snapshots.Create(ctx, gameA); !errors.Is(err, snapshot.ErrUnchanged) {
		t.Fatalf("unchanged backup should be skipped: %v", err)
	}
	if err := a.syncer.Upload(ctx, r2, first); err != nil {
		t.Fatalf("computer A upload: %v", err)
	}
	assertRemoteSnapshot(t, ctx, a.store, first.ID)
	return first
}

func e2eImportAndRestore(t *testing.T, ctx context.Context, r2 *R2, b *e2eDevice, first core.Snapshot, sourceTemplate string) core.Game {
	t.Helper()
	result, err := NewReconciler(b.store).Reconcile(ctx, r2)
	if err != nil || result.Snapshots != 1 || result.Games != 1 {
		t.Fatalf("computer B import: result=%+v err=%v", result, err)
	}
	if _, err := b.store.BlobPath(ctx, first.Files[0].Hash); err == nil {
		t.Fatal("reconciliation downloaded a blob before restore")
	}
	result, err = NewReconciler(b.store).Reconcile(ctx, r2)
	if err != nil || result.Snapshots != 0 || result.Skipped != 1 {
		t.Fatalf("repeat import was not idempotent: result=%+v err=%v", result, err)
	}
	gameB, err := b.store.Game(ctx, b.gameID)
	if err != nil || gameB.Enabled || gameB.Store != "remote" {
		t.Fatalf("imported game should await local configuration: game=%+v err=%v", gameB, err)
	}
	b.addPath(t, ctx, sourceTemplate)
	b.writeSave(t, "B: current save")
	archiveDevice := newE2EDevice(t, ctx, "computer-b-export", b.gameID)
	if err := e2eExportRemoteSnapshot(t, ctx, r2, archiveDevice, first, "A: first save"); err != nil {
		t.Fatalf("computer B export without a local blob or save path: %v", err)
	}
	if err := b.download(t, ctx, r2, first); err != nil {
		t.Fatalf("computer B download: %v", err)
	}
	preRestore, err := b.snapshots.Restore(ctx, gameB, first.ID)
	if err != nil || preRestore.ID == "" {
		t.Fatalf("computer B restore should preserve its current save: pre=%+v err=%v", preRestore, err)
	}
	b.assertSave(t, "A: first save")
	if _, err := b.snapshots.Restore(ctx, gameB, preRestore.ID); err != nil {
		t.Fatalf("computer B undo restore: %v", err)
	}
	b.assertSave(t, "B: current save")
	return gameB
}

func e2eReverseSync(t *testing.T, ctx context.Context, r2 *R2, a, b *e2eDevice, gameA, gameB core.Game) core.Snapshot {
	t.Helper()
	b.writeSave(t, "B: newer save")
	second, err := b.snapshots.Create(ctx, gameB)
	if err != nil {
		t.Fatalf("computer B backup: %v", err)
	}
	if err := b.syncer.Upload(ctx, r2, second); err != nil {
		t.Fatalf("computer B upload: %v", err)
	}
	result, err := NewReconciler(a.store).Reconcile(ctx, r2)
	if err != nil || result.Snapshots < 1 || result.Skipped < 1 {
		t.Fatalf("computer A refresh: result=%+v err=%v", result, err)
	}
	if err := a.download(t, ctx, r2, second); err != nil {
		t.Fatalf("computer A download: %v", err)
	}
	if _, err := a.snapshots.Restore(ctx, gameA, second.ID); err != nil {
		t.Fatalf("computer A restore: %v", err)
	}
	a.assertSave(t, "B: newer save")
	return second
}

func e2eRejectCorruptBlob(t *testing.T, ctx context.Context, r2 *R2, gameID, sourceTemplate string, second core.Snapshot) {
	t.Helper()
	// A damaged remote blob must be rejected before replacing the save on a
	// third, fresh computer.
	key := fmt.Sprintf("blobs/sha256/%s/%s.zst", second.Files[0].Hash[:2], second.Files[0].Hash)
	if err := r2.Put(ctx, key, "application/octet-stream", "zstd", bytes.NewReader([]byte("corrupted")), int64(len("corrupted"))); err != nil {
		t.Fatal(err)
	}
	c := newE2EDevice(t, ctx, "computer-c", gameID)
	if _, err := NewReconciler(c.store).Reconcile(ctx, r2); err != nil {
		t.Fatal(err)
	}
	c.addPath(t, ctx, sourceTemplate)
	c.writeSave(t, "C: untouched save")
	if err := c.download(t, ctx, r2, second); err == nil {
		t.Fatal("corrupt remote blob was accepted")
	}
	c.assertSave(t, "C: untouched save")
}

func e2eS3Client(ctx context.Context, endpoint, account, accessKey, secret string) (*s3.Client, error) {
	configuration, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secret, "")),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
	)
	if err != nil {
		return nil, err
	}
	if endpoint == "" {
		endpoint = "https://" + account + ".r2.cloudflarestorage.com"
	}
	return s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	}), nil
}

type e2eDevice struct {
	store     *database.Store
	snapshots *snapshot.Service
	syncer    *Syncer
	saveDir   string
	blobDir   string
	gameID    string
}

func newE2EDevice(t *testing.T, ctx context.Context, name, gameID string) *e2eDevice {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	blobDir := filepath.Join(root, "blobs")
	return &e2eDevice{
		store: store, snapshots: snapshot.New(store, store, blobDir, name), syncer: NewSyncer(store),
		saveDir: filepath.Join(root, "saves"), blobDir: blobDir, gameID: gameID,
	}
}

func (d *e2eDevice) addPath(t *testing.T, ctx context.Context, template string) {
	t.Helper()
	if err := d.store.AddPath(ctx, core.GamePath{
		ID: "save-location", GameID: d.gameID, Source: "catalog", Template: template,
		Resolved: d.saveDir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func (d *e2eDevice) writeSave(t *testing.T, contents string) {
	t.Helper()
	if err := os.MkdirAll(d.saveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.saveDir, "slot.sav"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *e2eDevice) assertSave(t *testing.T, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(d.saveDir, "slot.sav"))
	if err != nil || string(got) != want {
		t.Fatalf("save contents: got=%q want=%q err=%v", got, want, err)
	}
}

func e2eExportRemoteSnapshot(t *testing.T, ctx context.Context, r2 *R2, device *e2eDevice, target core.Snapshot, want string) error {
	t.Helper()
	if _, err := NewReconciler(device.store).Reconcile(ctx, r2); err != nil {
		return err
	}
	if _, err := device.store.Snapshot(ctx, target.ID); err != nil {
		return err
	}
	if _, err := device.store.BlobPath(ctx, target.Files[0].Hash); err == nil {
		return fmt.Errorf("fresh export device unexpectedly has the snapshot blob")
	}
	needed, err := device.syncer.NeedsDownload(ctx, target)
	if err != nil {
		return err
	}
	if !needed {
		return fmt.Errorf("remote snapshot was not reported as needing download")
	}
	if err := device.syncer.EnsureLocal(ctx, r2, target, device.blobDir); err != nil {
		return err
	}
	var archive bytes.Buffer
	if err := device.snapshots.Export(ctx, target.GameID, target.ID, &archive); err != nil {
		return err
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		return err
	}
	for _, entry := range reader.File {
		if filepath.Base(entry.Name) != "slot.sav" {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if string(data) != want {
			return fmt.Errorf("exported save = %q, want %q", data, want)
		}
		return nil
	}
	return fmt.Errorf("archive did not contain slot.sav")
}

func (d *e2eDevice) download(t *testing.T, ctx context.Context, r2 *R2, target core.Snapshot) error {
	t.Helper()
	needed, err := d.syncer.NeedsDownload(ctx, target)
	if err != nil {
		return err
	}
	if !needed {
		return fmt.Errorf("fresh device unexpectedly has remote blob")
	}
	return d.syncer.EnsureLocal(ctx, r2, target, d.blobDir)
}

func assertRemoteSnapshot(t *testing.T, ctx context.Context, store *database.Store, id string) {
	t.Helper()
	got, err := store.Snapshot(ctx, id)
	if err != nil || got.RemoteState != "synced" {
		t.Fatalf("snapshot remote state: state=%q err=%v", got.RemoteState, err)
	}
}

func cleanupE2EObjects(t *testing.T, client *s3.Client, bucket, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
		if err != nil {
			t.Errorf("list E2E objects for cleanup: %v", err)
			return
		}
		if len(list.Contents) == 0 {
			return
		}
		for _, object := range list.Contents {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key}); err != nil {
				t.Errorf("delete E2E object %q: %v", aws.ToString(object.Key), err)
				return
			}
		}
	}
}
