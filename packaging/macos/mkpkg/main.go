// Command mkpkg builds a macOS installer package (.pkg) from a directory, on any system: the
// release pipeline runs on Linux, where Apple's pkgbuild doesn't exist.
//
//	go run ./packaging/macos/mkpkg -root root -id org.example.tool -version 1.0.0 -o tool.pkg
//
// It writes a flat component package, the kind pkgbuild makes: a xar archive holding
// PackageInfo, a Bom (the bill of materials: every path with its mode, owner, size and
// checksum) and a Payload (gzip'd cpio). Everything is owned by root:wheel and installed
// relative to "/". The package is unsigned.
package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func main() {
	root := flag.String("root", "", "directory whose contents are installed relative to /")
	id := flag.String("id", "", "package identifier, e.g. org.example.tool")
	version := flag.String("version", "", "package version")
	out := flag.String("o", "", "output .pkg")
	flag.Parse()
	if *root == "" || *id == "" || *version == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	entries, err := scan(*root)
	if err != nil {
		log.Fatal(err)
	}
	pkg, err := build(entries, *id, *version, time.Now())
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, pkg, 0o644); err != nil {
		log.Fatal(err)
	}
}

// entry is one path of the payload.
type entry struct {
	path  string // "." or "./usr/local/bin/cu"
	dir   bool
	mode  fs.FileMode
	mtime time.Time
	data  []byte
}

// scan reads the tree in the order Apple's tools number it: depth first, and within a
// directory its files (by name) before its subdirectories (by name).
func scan(root string) ([]entry, error) {
	var out []entry
	var walk func(rel string) error
	walk = func(rel string) error {
		info, err := os.Lstat(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		e := entry{path: "./" + filepath.ToSlash(rel), dir: info.IsDir(), mode: info.Mode(), mtime: info.ModTime()}
		if rel == "." {
			e.path = "."
		}
		switch {
		case info.Mode().IsRegular():
			if e.data, err = os.ReadFile(filepath.Join(root, rel)); err != nil {
				return err
			}
		case !info.IsDir():
			return fmt.Errorf("%s: only files and directories are supported", rel)
		}
		out = append(out, e)
		if !info.IsDir() {
			return nil
		}
		children, err := os.ReadDir(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		slices.SortStableFunc(children, func(a, b fs.DirEntry) int {
			if a.IsDir() != b.IsDir() {
				if a.IsDir() {
					return 1
				}
				return -1
			}
			return strings.Compare(a.Name(), b.Name())
		})
		for _, c := range children {
			if err := walk(filepath.Join(rel, c.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(".")
}

func build(entries []entry, id, version string, now time.Time) ([]byte, error) {
	bom, err := makeBom(entries)
	if err != nil {
		return nil, err
	}
	payload, err := makePayload(entries)
	if err != nil {
		return nil, err
	}
	var kbytes int
	for _, e := range entries {
		kbytes += (len(e.data) + 1023) / 1024
	}
	info := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<pkg-info overwrite-permissions="true" relocatable="false" identifier="%s" postinstall-action="none" version="%s" format-version="2" install-location="/" auth="root">
    <payload numberOfFiles="%d" installKBytes="%d"/>
    <bundle-version/>
    <upgrade-bundle/>
    <update-bundle/>
    <atomic-update-bundle/>
    <strict-identifier/>
    <relocate/>
</pkg-info>
`, xmlEscape(id), xmlEscape(version), len(entries), kbytes)
	return makeXar([]xarFile{{"PackageInfo", []byte(info)}, {"Bom", bom}, {"Payload", payload}}, now)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// --- Payload: gzip'd cpio in the "odc" format, owned by root:wheel -----------------------------

func makePayload(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	write := func(ino int, mode uint32, mtime int64, name string, data []byte) {
		fmt.Fprintf(gz, "070707%06o%06o%06o%06o%06o%06o%06o%011o%06o%011o%s\x00",
			0, ino, mode, 0, 0, 1, 0, mtime, len(name)+1, len(data), name)
		gz.Write(data)
	}
	for i, e := range entries {
		write(i+1, cpioMode(e), e.mtime.Unix(), e.path, e.data)
	}
	write(0, 0, 0, "TRAILER!!!", nil)
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// cpioMode is the Unix st_mode: type bits plus permissions.
func cpioMode(e entry) uint32 {
	perm := uint32(e.mode.Perm())
	if e.dir {
		return 0o040000 | perm
	}
	return 0o100000 | perm
}

// --- Bom: Apple's bill of materials ------------------------------------------------------------
//
// A BOMStore file: a header, numbered blocks, named variables pointing at blocks, and an index
// of the blocks' addresses. The variables are BomInfo, Paths (a B-tree of every path),
// HLIndex (hard links), VIndex and Size64 (files over 4 GB), the last three empty here.
// Everything is big-endian.

type bomWriter struct {
	data   bytes.Buffer
	blocks [][2]uint32 // address, length; block 0 is always null
}

func (w *bomWriter) add(b []byte) uint32 {
	w.blocks = append(w.blocks, [2]uint32{uint32(w.data.Len()), uint32(len(b))})
	w.data.Write(b)
	return uint32(len(w.blocks) - 1)
}

func be(vs ...any) []byte {
	var b bytes.Buffer
	for _, v := range vs {
		binary.Write(&b, binary.BigEndian, v)
	}
	return b.Bytes()
}

// tree is a BOMTree header pointing at its (only) node.
func tree(child, blockSize, pathCount uint32) []byte {
	return append([]byte("tree"), be(uint32(1), child, blockSize, pathCount, uint8(0))...)
}

// node is a B-tree node padded to the tree's block size.
func node(isLeaf, count uint16, pairs [][2]uint32, size int) []byte {
	b := be(isLeaf, count, uint32(0), uint32(0))
	for _, p := range pairs {
		b = append(b, be(p[0], p[1])...)
	}
	return append(b, make([]byte, size-len(b))...)
}

const bomBlockSize = 4096

func makeBom(entries []entry) ([]byte, error) {
	if 12+8*len(entries) > bomBlockSize {
		return nil, fmt.Errorf("%d paths don't fit one Bom node; mkpkg only writes small packages", len(entries))
	}
	w := &bomWriter{blocks: [][2]uint32{{0, 0}}}
	w.data.Write(make([]byte, 512)) // the header goes here at the end

	// Every path: its info (type, mode, owner, size, checksum), its name with its parent's id,
	// and a small record tying the two to the path's id.
	type key struct {
		parent uint32
		name   string
		info   uint32 // block of the id record
		file   uint32 // block of the name
	}
	ids := map[string]uint32{}
	var keys []key
	var total uint32
	for i, e := range entries {
		id := uint32(i + 1)
		ids[path.Clean(e.path)] = id // path.Dir("./usr/local") is "usr": key on clean paths
		typ, size, sum := uint8(1), uint32(len(e.data)), cksum(e.data)
		if e.dir {
			typ, size, sum = 2, 0, 0
		} else {
			total += size
		}
		info2 := be(typ, uint8(1), uint16(0x000f), uint16(cpioMode(e)), uint32(0), uint32(0),
			uint32(e.mtime.Unix()), size, uint8(1), sum, uint32(0))
		infoBlock := w.add(info2)
		name, parent := path.Base(e.path), ids[path.Dir(path.Clean(e.path))]
		if e.path == "." {
			name, parent = ".", 0
		}
		fileBlock := w.add(append(be(parent), append([]byte(name), 0)...))
		keys = append(keys, key{parent, name, w.add(be(id, infoBlock)), fileBlock})
	}
	slices.SortFunc(keys, func(a, b key) int {
		if a.parent != b.parent {
			return int(a.parent) - int(b.parent)
		}
		return strings.Compare(a.name, b.name)
	})
	pairs := make([][2]uint32, len(keys))
	for i, k := range keys {
		pairs[i] = [2]uint32{k.info, k.file}
	}

	bomInfo := w.add(be(uint32(1), uint32(len(entries)+1), uint32(1), uint32(0), uint32(0), total, uint32(0)))
	paths := w.add(tree(w.add(node(1, uint16(len(pairs)), pairs, bomBlockSize)), bomBlockSize, uint32(len(pairs))))
	hlIndex := w.add(tree(w.add(node(1, 0, nil, bomBlockSize)), bomBlockSize, 0))
	vIndex := w.add(be(uint32(1), w.add(tree(w.add(node(1, 0, nil, 128)), 128, 0)), uint32(0), uint8(0)))
	size64 := w.add(tree(w.add(node(1, 0, nil, bomBlockSize)), bomBlockSize, 0))

	varsOffset := uint32(w.data.Len())
	vars := be(uint32(5))
	for _, v := range []struct {
		name  string
		block uint32
	}{{"BomInfo", bomInfo}, {"Paths", paths}, {"HLIndex", hlIndex}, {"VIndex", vIndex}, {"Size64", size64}} {
		vars = append(vars, be(v.block, uint8(len(v.name)))...)
		vars = append(vars, v.name...)
	}
	w.data.Write(vars)

	indexOffset := uint32(w.data.Len())
	index := be(uint32(len(w.blocks)))
	for _, b := range w.blocks {
		index = append(index, be(b[0], b[1])...)
	}
	index = append(index, be(uint32(2), uint64(0), uint64(0))...) // an empty free list
	w.data.Write(index)

	out := w.data.Bytes()
	copy(out, append([]byte("BOMStore"), be(uint32(1), uint32(len(w.blocks)-1),
		indexOffset, uint32(len(index)), varsOffset, uint32(len(vars)))...))
	return out, nil
}

// cksum is the POSIX cksum(1) CRC, which the Bom stores per file.
func cksum(data []byte) uint32 {
	var crc uint32
	step := func(b byte) {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	for _, b := range data {
		step(b)
	}
	for n := len(data); n > 0; n >>= 8 {
		step(byte(n))
	}
	return ^crc
}

// --- xar: the archive a flat package is -------------------------------------------------------

type xarFile struct {
	name string
	data []byte
}

// makeXar stores the files uncompressed after the table of contents' own SHA-1, which the
// header says the heap starts with.
func makeXar(files []xarFile, now time.Time) ([]byte, error) {
	var toc strings.Builder
	stamp := now.UTC().Format("2006-01-02T15:04:05Z")
	fmt.Fprintf(&toc, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<xar>\n <toc>\n"+
		"  <checksum style=\"sha1\">\n   <size>20</size>\n   <offset>0</offset>\n  </checksum>\n"+
		"  <creation-time>%s</creation-time>\n", now.UTC().Format("2006-01-02T15:04:05"))
	offset := sha1.Size
	for i, f := range files {
		sum := sha1.Sum(f.data)
		fmt.Fprintf(&toc, "  <file id=\"%d\">\n   <name>%s</name>\n   <type>file</type>\n"+
			"   <mode>0644</mode>\n   <uid>0</uid>\n   <user>root</user>\n   <gid>0</gid>\n   <group>wheel</group>\n"+
			"   <mtime>%s</mtime>\n   <ctime>%s</ctime>\n   <data>\n"+
			"    <archived-checksum style=\"sha1\">%s</archived-checksum>\n"+
			"    <extracted-checksum style=\"sha1\">%s</extracted-checksum>\n"+
			"    <encoding style=\"application/octet-stream\"/>\n"+
			"    <size>%d</size>\n    <offset>%d</offset>\n    <length>%d</length>\n   </data>\n  </file>\n",
			i+1, f.name, stamp, stamp, hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:]), len(f.data), offset, len(f.data))
		offset += len(f.data)
	}
	toc.WriteString(" </toc>\n</xar>\n")

	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte(toc.String()))
	if err := zw.Close(); err != nil {
		return nil, err
	}
	tocSum := sha1.Sum(z.Bytes())

	var out bytes.Buffer
	out.Write(be([]byte("xar!"), uint16(28), uint16(1), uint64(z.Len()), uint64(toc.Len()), uint32(1)))
	out.Write(z.Bytes())
	out.Write(tocSum[:])
	for _, f := range files {
		out.Write(f.data)
	}
	return out.Bytes(), nil
}
