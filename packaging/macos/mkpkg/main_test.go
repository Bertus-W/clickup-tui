package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cksum must match POSIX cksum(1): the Bom stores it and installers check it.
func TestCksum(t *testing.T) {
	for data, want := range map[string]uint32{"": 4294967295, "hello\n": 3015617425, "x\n": 2192966820} {
		if got := cksum([]byte(data)); got != want {
			t.Errorf("cksum(%q) = %d, want %d", data, got, want)
		}
	}
}

// A package is a xar archive whose Bom lists every path under its parent.
func TestBuild(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "cu"), []byte("hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.path)
	}
	if want := []string{".", "./usr", "./usr/local", "./usr/local/bin", "./usr/local/bin/cu"}; !equal(paths, want) {
		t.Fatalf("paths %v, want %v", paths, want)
	}
	pkg, err := build(entries, "org.example.test", "1.0.0", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pkg, []byte("xar!")) || binary.BigEndian.Uint16(pkg[4:]) != 28 {
		t.Fatalf("not a xar archive: % x", pkg[:8])
	}
	bom, err := makeBom(entries)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(bom, []byte("BOMStore")) {
		t.Fatal("not a Bom")
	}
	// Every name record carries its parent's id: . is 1, usr 2, local 3, bin 4.
	for name, parent := range map[string]uint32{"usr": 1, "local": 2, "bin": 3, "cu": 4} {
		rec := append(binary.BigEndian.AppendUint32(nil, parent), append([]byte(name), 0)...)
		if !bytes.Contains(bom, rec) {
			t.Errorf("no record for %s under parent %d", name, parent)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
