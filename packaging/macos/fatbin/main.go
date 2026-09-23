// Command fatbin joins Mach-O binaries into one universal binary, like `lipo -create`, on any
// system (the release pipeline runs on Linux, where lipo doesn't exist).
//
//	go run ./packaging/macos/fatbin -o cu darwin_amd64/cu darwin_arm64/cu
//
// The layout matches Apple's lipo: a big-endian fat header, then each slice at an offset
// aligned to its page size (4 KB for x86_64, 16 KB for arm64). Each slice keeps its own code
// signature, so Go's ad-hoc signed arm64 binaries still run.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
)

const (
	machoMagic64 = 0xfeedfacf // little-endian 64-bit Mach-O, as Go writes for amd64 and arm64
	fatMagic     = 0xcafebabe
	cpuX86_64    = 0x01000007
	cpuARM64     = 0x0100000c
)

func main() {
	out := flag.String("o", "", "output universal binary")
	flag.Parse()
	if *out == "" || flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: fatbin -o out binary1 binary2 [...]")
		os.Exit(2)
	}
	var slices [][]byte
	for _, path := range flag.Args() {
		b, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		slices = append(slices, b)
	}
	fat, err := join(slices)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, fat, 0o755); err != nil {
		log.Fatal(err)
	}
}

func join(binaries [][]byte) ([]byte, error) {
	type arch struct {
		cpu, sub, offset, size, align uint32
		data                          []byte
	}
	var archs []arch
	offset := uint32(8 + 20*len(binaries))
	for i, b := range binaries {
		if len(b) < 12 || binary.LittleEndian.Uint32(b) != machoMagic64 {
			return nil, fmt.Errorf("binary %d isn't a 64-bit Mach-O file", i+1)
		}
		cpu, sub := binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:])
		align := uint32(12) // 4 KB pages
		switch cpu {
		case cpuARM64:
			align = 14 // 16 KB pages
		case cpuX86_64:
		default:
			return nil, fmt.Errorf("binary %d: unknown CPU type %#x", i+1, cpu)
		}
		offset = (offset + 1<<align - 1) &^ (1<<align - 1)
		archs = append(archs, arch{cpu, sub, offset, uint32(len(b)), align, b})
		offset += uint32(len(b))
	}
	var out bytes.Buffer
	binary.Write(&out, binary.BigEndian, []uint32{fatMagic, uint32(len(archs))})
	for _, a := range archs {
		binary.Write(&out, binary.BigEndian, []uint32{a.cpu, a.sub, a.offset, a.size, a.align})
	}
	for _, a := range archs {
		out.Write(make([]byte, int(a.offset)-out.Len()))
		out.Write(a.data)
	}
	return out.Bytes(), nil
}
