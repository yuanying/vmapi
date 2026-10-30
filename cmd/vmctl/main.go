// Command vmctl is the command-line client of vmapi.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/client"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const defaultServer = "https://vm.poissonnerie.dev"

const usage = `vmctl talks to vmapi through devbox proxy.

Usage:
  vmctl [global flags] <command> [flags] [arguments]

Commands:
  whoami                              who vmapi takes you for
  images                              the images a VM can be created from
  list [--owner NAME]                 every VM, or one owner's
  get NAME                            one VM
  create NAME --image IMAGE [--vcpus N] [--memory-mib N] [--disk-gib N]
         [--ip IP] [--user-data FILE|-]
                                      create and start a VM
  start NAME                          start a VM
  stop NAME [--force]                 ask the guest to shut down, or force it off
  delete NAME                         delete a VM, its disk and its snapshots
  snapshot list NAME                  the VM's snapshots
  snapshot create NAME SNAPSHOT       take a snapshot
  snapshot revert NAME SNAPSHOT       return the VM to a snapshot
  snapshot delete NAME SNAPSHOT       delete a snapshot

Global flags:
  --server URL        vmapi's URL ($VMCTL_SERVER, default ` + defaultServer + `)
  --token-file PATH   the file holding the proxy token, mode 0600
                      ($VMCTL_TOKEN_FILE, default $XDG_CONFIG_HOME/vmctl/token)
  -o, --output FMT    table (default) or json, the API's own answer
  --version           print the version

The token can also be given in $VMCTL_TOKEN. It is never printed.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// usageError is a mistake in the command line: exit 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	client         *client.Client
	json           bool
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vmctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	showVersion := fs.Bool("version", false, "")
	server := fs.String("server", "", "")
	tokenFile := fs.String("token-file", "", "")
	var output string
	fs.StringVar(&output, "output", "table", "")
	fs.StringVar(&output, "o", "table", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprint(stdout, usage)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "vmctl: %v\n\n%s", err, usage)
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "vmctl %s\n", version)
		return 0
	}
	if fs.NArg() == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	if output != "table" && output != "json" {
		_, _ = fmt.Fprintf(stderr, "vmctl: --output is table or json, not %q\n", output)
		return 2
	}

	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr, json: output == "json"}
	var token string
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := func() error {
		cmd, ok := commands[fs.Arg(0)]
		if !ok {
			return usagef("unknown command %q", fs.Arg(0))
		}
		var err error
		if token, err = readToken(*tokenFile); err != nil {
			return err
		}
		c.client = &client.Client{BaseURL: serverURL(*server), Token: token}
		return cmd(ctx, c, fs.Args()[1:])
	}()
	if err == nil {
		return 0
	}
	redact := func(s string) string {
		if token == "" {
			return s
		}
		return strings.ReplaceAll(s, token, "[token]")
	}
	msg := redact(err.Error())
	var ue usageError
	if errors.As(err, &ue) {
		_, _ = fmt.Fprintf(stderr, "vmctl: %s\n\nRun vmctl with no arguments for help.\n", msg)
		return 2
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		_, _ = fmt.Fprintf(stderr, "vmctl: %s: %s\n", ae.Code, redact(ae.Message))
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "vmctl: %s\n", msg)
	return 1
}

func serverURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("VMCTL_SERVER"); v != "" {
		return v
	}
	return defaultServer
}

func defaultTokenFile() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "vmctl", "token")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "vmctl", "token")
}

// readToken finds the token: --token-file, then $VMCTL_TOKEN, then
// $VMCTL_TOKEN_FILE, then the default file. A file that others can read is
// refused, as the token is a key to every VM of its user.
func readToken(flagFile string) (string, error) {
	path := flagFile
	if path == "" {
		if t := os.Getenv("VMCTL_TOKEN"); t != "" {
			return t, nil
		}
		path = os.Getenv("VMCTL_TOKEN_FILE")
	}
	explicit := path != ""
	if !explicit {
		path = defaultTokenFile()
	}
	info, err := os.Stat(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no token: write it to %s (mode 0600), or set VMCTL_TOKEN", path)
		}
		return "", fmt.Errorf("token file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s is readable by others (%#o); make it 0600", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return token, nil
}

// parse parses flags and arguments in any order, and wants n arguments.
func parse(fs *flag.FlagSet, args []string, names ...string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usagef("%s: %v", fs.Name(), err)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) != len(names) {
		want := strings.ToUpper(strings.Join(names, " "))
		return nil, usagef("%s wants %s", fs.Name(), strings.TrimSpace(fs.Name()+" "+want))
	}
	return positional, nil
}

type command func(ctx context.Context, c *cli, args []string) error

var commands = map[string]command{
	"whoami":   whoami,
	"images":   images,
	"list":     list,
	"get":      get,
	"create":   create,
	"start":    start,
	"stop":     stop,
	"delete":   deleteVM,
	"snapshot": snapshot,
}

// call sends the request and prints the answer: as it came with -o json,
// or through table otherwise.
func call[T any](ctx context.Context, c *cli, method, path string, body any, table func(w io.Writer, v T)) (*client.Response, error) {
	resp, err := c.client.Do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if len(resp.Body) == 0 {
		return resp, nil
	}
	if c.json {
		var v any
		if err := json.Unmarshal(resp.Body, &v); err != nil {
			return nil, err
		}
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return resp, enc.Encode(v)
	}
	var v T
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		return nil, err
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	table(tw, v)
	return resp, tw.Flush()
}

func whoami(ctx context.Context, c *cli, args []string) error {
	if _, err := parse(flag.NewFlagSet("whoami", flag.ContinueOnError), args); err != nil {
		return err
	}
	_, err := call(ctx, c, "GET", "/api/v1/whoami", nil, func(w io.Writer, v api.Whoami) {
		_, _ = fmt.Fprintf(w, "USER\tADMIN\n%s\t%v\n", v.User, v.Admin)
	})
	return err
}

func images(ctx context.Context, c *cli, args []string) error {
	if _, err := parse(flag.NewFlagSet("images", flag.ContinueOnError), args); err != nil {
		return err
	}
	_, err := call(ctx, c, "GET", "/api/v1/images", nil, func(w io.Writer, v api.ImageList) {
		_, _ = fmt.Fprintln(w, "NAME\tDESCRIPTION")
		for _, img := range v.Images {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", img.Name, img.Description)
		}
	})
	return err
}

func vmTable(w io.Writer, vms ...api.VM) {
	_, _ = fmt.Fprintln(w, "NAME\tOWNER\tIP\tSTATE\tIMAGE\tVCPUS\tMEMORY\tDISK\tCREATED")
	for _, v := range vms {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%dMiB\t%dGiB\t%s\n",
			v.Name, v.Owner, v.IP, v.State, v.Image, v.VCPUs, v.MemoryMiB, v.DiskGiB, v.CreatedAt.Format("2006-01-02 15:04"))
	}
}

func list(ctx context.Context, c *cli, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	owner := fs.String("owner", "", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	path := "/api/v1/vms"
	if *owner != "" {
		path += "?owner=" + url.QueryEscape(*owner)
	}
	_, err := call(ctx, c, "GET", path, nil, func(w io.Writer, v api.VMList) { vmTable(w, v.VMs...) })
	return err
}

func get(ctx context.Context, c *cli, args []string) error {
	pos, err := parse(flag.NewFlagSet("get", flag.ContinueOnError), args, "name")
	if err != nil {
		return err
	}
	_, err = call(ctx, c, "GET", client.VMPath(pos[0]), nil, func(w io.Writer, v api.VM) { vmTable(w, v) })
	return err
}

// optionalInt is an int flag that tells "not given" from zero.
type optionalInt struct{ v *int }

func (o *optionalInt) String() string {
	if o.v == nil {
		return ""
	}
	return fmt.Sprint(*o.v)
}

func (o *optionalInt) Set(s string) error {
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil {
		return fmt.Errorf("not a number: %q", s)
	}
	o.v = &n
	return nil
}

func create(ctx context.Context, c *cli, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	img := fs.String("image", "", "")
	ip := fs.String("ip", "", "")
	userData := fs.String("user-data", "", "")
	var vcpus, memory, disk optionalInt
	fs.Var(&vcpus, "vcpus", "")
	fs.Var(&memory, "memory-mib", "")
	fs.Var(&disk, "disk-gib", "")
	pos, err := parse(fs, args, "name")
	if err != nil {
		return err
	}
	if *img == "" {
		return usagef("create wants --image; vmctl images lists them")
	}
	req := api.CreateVMRequest{Name: pos[0], Image: *img, VCPUs: vcpus.v, MemoryMiB: memory.v, DiskGiB: disk.v}
	if *ip != "" {
		req.IP = ip
	}
	if *userData != "" {
		var data []byte
		if *userData == "-" {
			data, err = io.ReadAll(c.stdin)
		} else {
			data, err = os.ReadFile(*userData)
		}
		if err != nil {
			return fmt.Errorf("user-data: %w", err)
		}
		s := string(data)
		req.UserData = &s
	}
	_, err = call(ctx, c, "POST", "/api/v1/vms", req, func(w io.Writer, v api.VM) { vmTable(w, v) })
	return err
}

func start(ctx context.Context, c *cli, args []string) error {
	pos, err := parse(flag.NewFlagSet("start", flag.ContinueOnError), args, "name")
	if err != nil {
		return err
	}
	_, err = call(ctx, c, "POST", client.VMPath(pos[0], "start"), nil, func(w io.Writer, v api.VM) { vmTable(w, v) })
	return err
}

func stop(ctx context.Context, c *cli, args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	force := fs.Bool("force", false, "")
	pos, err := parse(fs, args, "name")
	if err != nil {
		return err
	}
	resp, err := call(ctx, c, "POST", client.VMPath(pos[0], "stop"), api.StopVMRequest{Force: *force}, func(w io.Writer, v api.VM) { vmTable(w, v) })
	if err == nil && resp.Status == 202 && !c.json {
		_, _ = fmt.Fprintf(c.stderr, "asked %s to shut down; vmctl get %s shows when it is shut_off, vmctl stop --force %s turns it off at once\n", pos[0], pos[0], pos[0])
	}
	return err
}

func deleteVM(ctx context.Context, c *cli, args []string) error {
	pos, err := parse(flag.NewFlagSet("delete", flag.ContinueOnError), args, "name")
	if err != nil {
		return err
	}
	_, err = c.client.Do(ctx, "DELETE", client.VMPath(pos[0]), nil)
	return err
}

func snapshotTable(w io.Writer, snaps ...api.Snapshot) {
	_, _ = fmt.Fprintln(w, "NAME\tVM STATE\tCREATED")
	for _, s := range snaps {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.VMState, s.CreatedAt.Format("2006-01-02 15:04:05"))
	}
}

func snapshot(ctx context.Context, c *cli, args []string) error {
	if len(args) == 0 {
		return usagef("snapshot wants list, create, revert or delete")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("snapshot "+sub, flag.ContinueOnError)
	switch sub {
	case "list":
		pos, err := parse(fs, args, "name")
		if err != nil {
			return err
		}
		_, err = call(ctx, c, "GET", client.VMPath(pos[0], "snapshots"), nil, func(w io.Writer, v api.SnapshotList) { snapshotTable(w, v.Snapshots...) })
		return err
	case "create":
		pos, err := parse(fs, args, "name", "snapshot")
		if err != nil {
			return err
		}
		_, err = call(ctx, c, "POST", client.VMPath(pos[0], "snapshots"), api.CreateSnapshotRequest{Name: pos[1]}, func(w io.Writer, v api.Snapshot) { snapshotTable(w, v) })
		return err
	case "revert":
		pos, err := parse(fs, args, "name", "snapshot")
		if err != nil {
			return err
		}
		_, err = call(ctx, c, "POST", client.VMPath(pos[0], "snapshots", pos[1], "revert"), nil, func(w io.Writer, v api.VM) { vmTable(w, v) })
		return err
	case "delete":
		pos, err := parse(fs, args, "name", "snapshot")
		if err != nil {
			return err
		}
		_, err = c.client.Do(ctx, "DELETE", client.VMPath(pos[0], "snapshots", pos[1]), nil)
		return err
	default:
		return usagef("snapshot wants list, create, revert or delete, not %q", sub)
	}
}
