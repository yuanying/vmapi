// Package image downloads the OS images of the configuration, checks them
// against their SHA-256 and keeps them under the name of that checksum
// (docs/adr/0004).
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/yuanying/vmapi/internal/config"
)

// Store keeps images in Dir.
type Store struct {
	Dir    string
	Client *http.Client

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Path is where the image is kept once downloaded.
func (s *Store) Path(img config.Image) string {
	return filepath.Join(s.Dir, img.SHA256+".img")
}

// Ensure returns the path of the image, downloading it first if it is not
// kept yet. Only a file whose checksum matches is kept; it is written to a
// temporary file and renamed, so a failure leaves nothing behind. Two calls
// for the same image do not download it twice.
func (s *Store) Ensure(ctx context.Context, img config.Image) (string, error) {
	lock := s.lock(img.SHA256)
	lock.Lock()
	defer lock.Unlock()

	path := s.Path(img)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := s.download(ctx, img, path); err != nil {
		return "", fmt.Errorf("image %s: %w", img.Name, err)
	}
	return path, nil
}

func (s *Store) lock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	if s.locks[key] == nil {
		s.locks[key] = &sync.Mutex{}
	}
	return s.locks[key]
}

func (s *Store) download(ctx context.Context, img config.Image, path string) (err error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, img.URL, nil)
	if err != nil {
		return err
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", img.URL, resp.Status)
	}

	tmp, err := os.CreateTemp(s.Dir, ".download-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return fmt.Errorf("GET %s: %w", img.URL, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != img.SHA256 {
		return fmt.Errorf("GET %s: sha256 is %s, want %s", img.URL, got, img.SHA256)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
