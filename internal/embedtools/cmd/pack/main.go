// Command pack is the build-time step behind scripts/embed-tools.sh:
//
//	pack -src DIR -licenses DIR -notices FILE -out DIR name=version...
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/andrew-bierman/abacus/internal/embedtools/packer"
)

func main() {
	var in packer.Input
	flag.StringVar(&in.Src, "src", "", "directory holding the verified executables")
	flag.StringVar(&in.Licenses, "licenses", "", "directory of <name>/LICENSE")
	flag.StringVar(&in.Notices, "notices", "", "THIRD_PARTY_NOTICES.md")
	flag.StringVar(&in.Out, "out", "", "output blob directory (new or empty)")
	flag.Parse()
	for _, a := range flag.Args() {
		n, v, ok := strings.Cut(a, "=")
		if !ok {
			fmt.Fprintln(os.Stderr, "pack: want name=version, got", a)
			os.Exit(2)
		}
		in.Tools = append(in.Tools, packer.Tool{Name: n, Version: v})
	}
	if in.Src == "" || in.Out == "" || in.Notices == "" || len(in.Tools) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pack -src DIR -licenses DIR -notices FILE -out DIR name=version...")
		os.Exit(2)
	}
	if err := packer.Pack(in); err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
}
