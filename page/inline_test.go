package page

import (
	"os/exec"
	"strings"
	"testing"
)

// mustInline lists hot-path functions that must stay inlinable. If one stops
// fitting Go's inlining budget (80), every caller pays a real CALL plus spills,
// and no other test would fail.
var mustInline = []string{
	"recordOK", // cost 79: one node under the budget
	"keyHead",
	"cmpHead",
}

func TestHotPathInlined(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the compiler; skipped with -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not found")
	}
	// -gcflags=-m prints the compiler's inlining decisions for this package.
	out, err := exec.Command(goBin, "build", "-gcflags=-m", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build -gcflags=-m: %v\n%s", err, out)
	}
	for _, fn := range mustInline {
		if !strings.Contains(string(out), "can inline "+fn+" ") &&
			!strings.Contains(string(out), "can inline "+fn+"\n") {
			t.Errorf("%s is no longer inlinable; check: go build -gcflags=-m=2 ./page/ 2>&1 | grep %s", fn, fn)
		}
	}
}
