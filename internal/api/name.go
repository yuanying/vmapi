package api

import "regexp"

// namePattern is the Name schema: a DNS label (docs/adr/0003).
var namePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidName reports whether s can name a VM, a snapshot or an image. Such a
// name carries no path separator and no "..", so it is safe in a path.
func ValidName(s string) bool {
	return namePattern.MatchString(s)
}
