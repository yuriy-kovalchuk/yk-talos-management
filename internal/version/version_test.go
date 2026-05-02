package version

import (
	"testing"
)

func TestGoVersion(t *testing.T) {
	v := GoVersion()
	if v == "" {
		t.Error("GoVersion() returned empty string")
	}
}