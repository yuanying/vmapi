// Command vmctl is the command-line client of vmapi.
//
// The API itself is not implemented yet; for now this only reports its
// version, so that the build, the release and the image can be exercised.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vmctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "vmctl %s\n", version)
		return 0
	}
	fs.Usage()
	return 2
}
