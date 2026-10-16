package page

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strings"
	"testing"
)

// typecheck compiles the real geometry.go (optionally edited) together with a
// fake pagesize file, in memory, and returns the first compile error (or nil).
func typecheck(t *testing.T, pageBits int, oldText, newText string) error {
	t.Helper()
	src, err := os.ReadFile("geometry.go")
	if err != nil {
		t.Fatal(err)
	}
	geo := string(src)
	if oldText != "" {
		if strings.Count(geo, oldText) != 1 {
			t.Fatalf("geometry.go must contain %q exactly once", oldText)
		}
		geo = strings.Replace(geo, oldText, newText, 1)
	}
	fake := fmt.Sprintf("package page\nconst PageBits = %d\n", pageBits)

	fset := token.NewFileSet()
	var files []*ast.File
	for name, text := range map[string]string{"geometry.go": geo, "pagesize.go": fake} {
		f, err := parser.ParseFile(fset, name, text, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: importer.Default()}
	_, err = conf.Check("page", fset, files, nil)
	return err
}

func TestGeometryAsserts(t *testing.T) {
	const fence = "MaxFenceBytes = 2 * MaxKeyLen"
	const bigFence = "MaxFenceBytes = 2048"
	cases := []struct {
		name      string
		pageBits  int
		old, new  string
		wantError string // "" = must compile
	}{
		{"4KiB ok", 12, "", "", ""},
		{"8KiB ok", 13, "", "", ""},
		{"2KiB breaks G6", 11, "", "", "-296"},   // 224 - 520
		{"16KiB breaks G1", 14, "", "", "-8192"}, // 8191 - 16383
		{"64KiB breaks G1", 16, "", "", "overflows"},
		{"fence 2048 at 4KiB breaks G6", 12, fence, bigFence, "-40"}, // 480 - 520
		{"fence 2048 at 8KiB ok", 13, fence, bigFence, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := typecheck(t, c.pageBits, c.old, c.new)
			switch {
			case c.wantError == "" && err != nil:
				t.Fatalf("want compile OK, got: %v", err)
			case c.wantError != "" && err == nil:
				t.Fatalf("want compile error containing %q, got none", c.wantError)
			case c.wantError != "" && !strings.Contains(err.Error(), c.wantError):
				t.Fatalf("want error containing %q, got: %v", c.wantError, err)
			}
		})
	}
}
