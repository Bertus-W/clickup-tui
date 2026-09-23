package main

import (
	"encoding/binary"
	"testing"
)

func macho(cpu uint32, size int) []byte {
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b, machoMagic64)
	binary.LittleEndian.PutUint32(b[4:], cpu)
	return b
}

// The slices sit where Apple's lipo puts them: x86_64 at 4 KB, arm64 at the next 16 KB boundary.
func TestJoin(t *testing.T) {
	fat, err := join([][]byte{macho(cpuX86_64, 5000), macho(cpuARM64, 100)})
	if err != nil {
		t.Fatal(err)
	}
	be := binary.BigEndian
	if be.Uint32(fat) != fatMagic || be.Uint32(fat[4:]) != 2 {
		t.Fatalf("header % x", fat[:8])
	}
	// fat_arch: cputype, cpusubtype, offset, size, align
	if off, align := be.Uint32(fat[16:]), be.Uint32(fat[24:]); off != 4096 || align != 12 {
		t.Errorf("x86_64 at %d, align 2^%d", off, align)
	}
	if off, align := be.Uint32(fat[36:]), be.Uint32(fat[44:]); off != 16384 || align != 14 {
		t.Errorf("arm64 at %d, align 2^%d", off, align)
	}
	if len(fat) != 16384+100 {
		t.Errorf("length %d", len(fat))
	}
	if _, err := join([][]byte{[]byte("#!/bin/sh\n..."), macho(cpuARM64, 100)}); err == nil {
		t.Error("a script isn't a Mach-O file")
	}
}
