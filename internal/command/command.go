// Package command runs the external programs vmapi needs: qemu-img for the
// disks (docs/adr/0006) and xorriso for the cloud-init seed (docs/adr/0005).
// They sit behind Runner, so tests check the arguments and the files without
// running anything.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Runner runs a program and returns its standard output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Exec runs programs for real.
type Exec struct{}

// Run runs the program. A failure carries what the program wrote to its
// standard error.
func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Disks makes and inspects disk images with qemu-img.
type Disks struct {
	Runner Runner
}

// ImageInfo is what qemu-img says about an image.
type ImageInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
	Backing     string `json:"backing-filename"`
}

// Inspect reads the format and the virtual size of an image. An image that
// has a backing file of its own is refused: a downloaded image could point
// it at any file on the host.
func (d Disks) Inspect(ctx context.Context, path string) (ImageInfo, error) {
	out, err := d.Runner.Run(ctx, "qemu-img", "info", "--output=json", path)
	if err != nil {
		return ImageInfo{}, err
	}
	var info ImageInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return ImageInfo{}, fmt.Errorf("qemu-img info %s: %w", path, err)
	}
	if info.Backing != "" {
		return ImageInfo{}, fmt.Errorf("image %s has a backing file of its own", path)
	}
	if info.Format == "" || info.VirtualSize <= 0 {
		return ImageInfo{}, fmt.Errorf("qemu-img info %s: no format or size", path)
	}
	return info, nil
}

// CreateOverlay makes a qcow2 disk of sizeGiB at path, backed by the image.
func (d Disks) CreateOverlay(ctx context.Context, backing, backingFormat, path string, sizeGiB int) error {
	_, err := d.Runner.Run(ctx, "qemu-img", "create", "-q", "-f", "qcow2",
		"-b", backing, "-F", backingFormat, path, strconv.Itoa(sizeGiB)+"G")
	return err
}

// Seeds makes cloud-init NoCloud seed ISOs with xorriso.
type Seeds struct {
	Runner Runner
}

// SeedFiles are the files of a seed.
type SeedFiles struct {
	UserData      []byte
	MetaData      []byte
	NetworkConfig []byte
}

// Make writes the seed ISO to out. The files are staged in a directory next
// to out, removed again whatever happens.
func (s Seeds) Make(ctx context.Context, out string, files SeedFiles) (err error) {
	dir, err := os.MkdirTemp(filepath.Dir(out), ".seed-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()

	paths := make([]string, 0, 3)
	for _, f := range []struct {
		name string
		data []byte
	}{{"user-data", files.UserData}, {"meta-data", files.MetaData}, {"network-config", files.NetworkConfig}} {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, f.data, 0o600); err != nil {
			return err
		}
		paths = append(paths, p)
	}
	args := append([]string{"-as", "mkisofs", "-output", out, "-volid", "cidata", "-joliet", "-rock"}, paths...)
	_, err = s.Runner.Run(ctx, "xorriso", args...)
	return err
}
