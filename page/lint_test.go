package page

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Page-size numbers that must be derived from PageBits, never typed by hand.
// Checked by value, so 0x2000 and 8_192 are caught as 8192.
var forbiddenSizes = map[int64]bool{
	4096: true, 8192: true, // PageSize
	4095: true, 8191: true, // PageSize-1
	4032: true, 8128: true, // Usable
	736: true, 1760: true, // MaxInlineRecord
	16384: true, // PagesPerSlab (4 KiB)
}

// allowedFiles may contain page-size numbers.
var allowedFiles = map[string]bool{
	"pagesize_8k.go": true,
	"pagesize_4k.go": true,
}

func TestNoPageSizeLiterals(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range names {
		if allowedFiles[name] || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0) // 0 = comments skipped
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return true
			}
			v, err := strconv.ParseInt(lit.Value, 0, 64)
			if err == nil && forbiddenSizes[v] {
				t.Errorf("%s: literal %s (= %d) must be derived from PageBits",
					fset.Position(lit.Pos()), lit.Value, v)
			}
			return true
		})
	}
}
