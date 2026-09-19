package binarycheck

import (
	"debug/elf"
	"os"
	"testing"
)

func TestStaticLinuxAMD64(t *testing.T) {
	path := os.Getenv("CTB_RELEASE_BINARY")
	if path == "" {
		t.Skip("set CTB_RELEASE_BINARY to audit the release artifact")
	}
	f, e := elf.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if f.Machine != elf.EM_X86_64 || f.Class != elf.ELFCLASS64 {
		t.Fatal("not Linux amd64 ELF")
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Fatal("requires dynamic loader")
		}
	}
	libs, e := f.ImportedLibraries()
	if e != nil {
		t.Fatal(e)
	}
	if len(libs) > 0 {
		t.Fatalf("shared libraries: %v", libs)
	}
	symbols, e := f.DynamicSymbols()
	if e != nil && e != elf.ErrNoSymbols {
		t.Fatal(e)
	}
	if len(symbols) > 0 {
		t.Fatal("unexpected dynamic symbols")
	}
}
