package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yuanying/vmapi/internal/config"
)

var content = []byte("not really a cloud image")

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func serve(t *testing.T, body []byte, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func onlyFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestEnsureDownloadsVerifiesAndKeeps(t *testing.T) {
	srv, hits := serve(t, content, http.StatusOK)
	dir := filepath.Join(t.TempDir(), "images")
	s := &Store{Dir: dir, Client: srv.Client()}
	img := config.Image{Name: "noble", URL: srv.URL + "/noble.img", SHA256: sum(content)}

	path, err := s.Ensure(context.Background(), img)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if want := filepath.Join(dir, sum(content)+".img"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(content) {
		t.Errorf("kept %q, %v", got, err)
	}

	// Kept: the second time does not download.
	if _, err := s.Ensure(context.Background(), img); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("downloaded %d times, want 1", hits.Load())
	}
}

func TestEnsureRejectsAChecksumMismatch(t *testing.T) {
	srv, _ := serve(t, content, http.StatusOK)
	dir := filepath.Join(t.TempDir(), "images")
	s := &Store{Dir: dir, Client: srv.Client()}
	img := config.Image{Name: "noble", URL: srv.URL, SHA256: sum([]byte("something else"))}
	if _, err := s.Ensure(context.Background(), img); err == nil {
		t.Fatal("Ensure kept an image whose checksum does not match")
	}
	if left := onlyFiles(t, dir); len(left) != 0 {
		t.Errorf("left behind %q", left)
	}
}

func TestEnsureFailsOnABadStatus(t *testing.T) {
	srv, _ := serve(t, []byte("gone"), http.StatusNotFound)
	dir := filepath.Join(t.TempDir(), "images")
	s := &Store{Dir: dir, Client: srv.Client()}
	if _, err := s.Ensure(context.Background(), config.Image{Name: "x", URL: srv.URL, SHA256: sum([]byte("gone"))}); err == nil {
		t.Fatal("Ensure succeeded on 404")
	}
	if left := onlyFiles(t, dir); len(left) != 0 {
		t.Errorf("left behind %q", left)
	}
}

func TestEnsureDownloadsOnceWhenAskedAtOnce(t *testing.T) {
	srv, hits := serve(t, content, http.StatusOK)
	s := &Store{Dir: filepath.Join(t.TempDir(), "images"), Client: srv.Client()}
	img := config.Image{Name: "noble", URL: srv.URL, SHA256: sum(content)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.Ensure(context.Background(), img); err != nil {
				t.Errorf("Ensure: %v", err)
			}
		})
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Errorf("downloaded %d times, want 1", hits.Load())
	}
}
