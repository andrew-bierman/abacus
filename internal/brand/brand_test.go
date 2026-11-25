package brand

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A product name literal must live only in this package, so renaming is mechanical.
func TestNameLiteralOnlyInBrand(t *testing.T) {
	names := []string{Name, Monitor}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if filepath.Base(p) == "brand.go" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "module ") || strings.Contains(t, "github.com/andrew-bierman/"+Name) {
				continue
			}
			for _, name := range names {
				if strings.Contains(t, `"`+name+`"`) {
					return &leak{path: p, name: name}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type leak struct {
	path string
	name string
}

func (l *leak) Error() string {
	return "product-name literal " + l.name + " outside internal/brand: " + l.path
}
