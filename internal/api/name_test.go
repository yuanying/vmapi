package api

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{"a", "k8s-test-cp1", "noble", "a1", strings.Repeat("a", 63)}
	for _, name := range valid {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "1a", "-a", "a-", "A", "a_b", "a.b", "a/b", "..", strings.Repeat("a", 64), "é"}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
	}
}
