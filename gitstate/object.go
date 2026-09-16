package gitstate

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// This file implements the small subset of git's object store that we need to
// read a commit's subject line without shelling out to git: loose objects,
// packfiles (v2 .idx), and ofs/ref deltas. It is intentionally read-only and
// best-effort -- any unsupported case returns an error so the caller can fall
// back to the git binary.

const (
	objCommit   = 1
	objTree     = 2
	objBlob     = 3
	objTag      = 4
	objOfsDelta = 6
	objRefDelta = 7
)

// readCommitSubject returns the subject line (first line of the commit message)
// for the given commit hash, reading objects directly from commonDir/objects.
func readCommitSubject(commonDir, hash string) (string, error) {
	typ, data, err := readObject(commonDir, hash, 0)
	if err != nil {
		return "", err
	}
	if typ != objCommit {
		return "", fmt.Errorf("object %s is not a commit (type %d)", hash, typ)
	}
	return commitSubject(data), nil
}

// commitSubject extracts the subject (first message line) from a raw commit
// object body. Commit objects are "<headers>\n\n<message>".
func commitSubject(commit []byte) string {
	idx := bytes.Index(commit, []byte("\n\n"))
	if idx < 0 {
		return ""
	}
	msg := commit[idx+2:]
	if nl := bytes.IndexByte(msg, '\n'); nl >= 0 {
		msg = msg[:nl]
	}
	return strings.TrimSpace(string(msg))
}

// readObject returns the type and inflated body of the object with the given
// hash. depth guards against pathological delta chains.
func readObject(commonDir, hash string, depth int) (byte, []byte, error) {
	if depth > 50 {
		return 0, nil, errors.New("delta chain too deep")
	}
	if len(hash) != 40 {
		return 0, nil, fmt.Errorf("unsupported hash %q", hash)
	}
	// Loose object first.
	loosePath := filepath.Join(commonDir, "objects", hash[:2], hash[2:])
	if f, err := os.Open(loosePath); err == nil {
		defer f.Close()
		zr, err := zlib.NewReader(bufio.NewReader(f))
		if err != nil {
			return 0, nil, err
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return 0, nil, err
		}
		nul := bytes.IndexByte(raw, 0)
		if nul < 0 {
			return 0, nil, errors.New("malformed loose object header")
		}
		hdr := string(raw[:nul])
		sp := strings.IndexByte(hdr, ' ')
		if sp < 0 {
			return 0, nil, errors.New("malformed loose object header")
		}
		return looseTypeCode(hdr[:sp]), raw[nul+1:], nil
	}
	return readPackedObject(commonDir, hash, depth)
}

func looseTypeCode(name string) byte {
	switch name {
	case "commit":
		return objCommit
	case "tree":
		return objTree
	case "blob":
		return objBlob
	case "tag":
		return objTag
	}
	return 0
}

// readPackedObject locates hash across the packfiles in commonDir/objects/pack
// and returns its type and inflated body.
func readPackedObject(commonDir, hash string, depth int) (byte, []byte, error) {
	packDir := filepath.Join(commonDir, "objects", "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return 0, nil, err
	}
	want, err := hex.DecodeString(hash)
	if err != nil {
		return 0, nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".idx") {
			continue
		}
		idxPath := filepath.Join(packDir, e.Name())
		off, ok, err := lookupPackOffset(idxPath, want)
		if err != nil || !ok {
			continue
		}
		packPath := strings.TrimSuffix(idxPath, ".idx") + ".pack"
		f, err := os.Open(packPath)
		if err != nil {
			return 0, nil, err
		}
		typ, data, err := readPackEntry(commonDir, f, int64(off), depth)
		f.Close()
		return typ, data, err
	}
	return 0, nil, fmt.Errorf("object %s not found", hash)
}

// lookupPackOffset finds the pack offset of want (a 20-byte SHA-1) in a v2
// pack index, returning (offset, found, error).
func lookupPackOffset(idxPath string, want []byte) (uint64, bool, error) {
	data, err := os.ReadFile(idxPath)
	if err != nil {
		return 0, false, err
	}
	// v2 idx: 4-byte magic 0xff744f63, 4-byte version (2).
	if len(data) < 8 || !bytes.Equal(data[:4], []byte{0xff, 0x74, 0x4f, 0x63}) ||
		binary.BigEndian.Uint32(data[4:8]) != 2 {
		return 0, false, errors.New("unsupported pack index format")
	}
	fanout := data[8 : 8+256*4]
	total := binary.BigEndian.Uint32(fanout[255*4:])
	lo := uint32(0)
	if want[0] > 0 {
		lo = binary.BigEndian.Uint32(fanout[(int(want[0])-1)*4:])
	}
	hi := binary.BigEndian.Uint32(fanout[int(want[0])*4:])
	namesStart := 8 + 256*4
	if int(total)*20 > len(data)-namesStart {
		return 0, false, errors.New("truncated pack index")
	}
	for lo < hi {
		mid := (lo + hi) / 2
		off := namesStart + int(mid)*20
		switch bytes.Compare(data[off:off+20], want) {
		case 0:
			return packOffsetAt(data, total, mid)
		case -1:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, false, nil
}

func packOffsetAt(data []byte, total, idx uint32) (uint64, bool, error) {
	namesStart := 8 + 256*4
	off4Start := namesStart + int(total)*20 + int(total)*4 // names + crc32
	if off4Start+int(idx)*4+4 > len(data) {
		return 0, false, errors.New("truncated pack index offset table")
	}
	o := binary.BigEndian.Uint32(data[off4Start+int(idx)*4:])
	if o&0x80000000 == 0 {
		return uint64(o), true, nil
	}
	// MSB set: index into the 8-byte large-offset table.
	off8Start := off4Start + int(total)*4
	big := o & 0x7fffffff
	if off8Start+int(big)*8+8 > len(data) {
		return 0, false, errors.New("bad large offset")
	}
	return binary.BigEndian.Uint64(data[off8Start+int(big)*8:]), true, nil
}

// readPackEntry reads the object at the given offset in an open packfile,
// resolving ofs/ref deltas against their bases.
func readPackEntry(commonDir string, f *os.File, offset int64, depth int) (byte, []byte, error) {
	if depth > 50 {
		return 0, nil, errors.New("delta chain too deep")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, nil, err
	}
	br := bufio.NewReader(f)
	c, err := br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	typ := (c >> 4) & 7
	// Object size varint (unused for decompression but consumes header bytes).
	for c&0x80 != 0 {
		if c, err = br.ReadByte(); err != nil {
			return 0, nil, err
		}
	}
	switch typ {
	case objCommit, objTree, objBlob, objTag:
		data, err := inflate(br)
		return typ, data, err
	case objOfsDelta:
		c, err := br.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		rel := uint64(c & 0x7f)
		for c&0x80 != 0 {
			if c, err = br.ReadByte(); err != nil {
				return 0, nil, err
			}
			rel = ((rel + 1) << 7) | uint64(c&0x7f)
		}
		delta, err := inflate(br)
		if err != nil {
			return 0, nil, err
		}
		baseType, baseData, err := readPackEntry(commonDir, f, offset-int64(rel), depth+1)
		if err != nil {
			return 0, nil, err
		}
		out, err := applyDelta(baseData, delta)
		return baseType, out, err
	case objRefDelta:
		var ref [20]byte
		if _, err := io.ReadFull(br, ref[:]); err != nil {
			return 0, nil, err
		}
		delta, err := inflate(br)
		if err != nil {
			return 0, nil, err
		}
		baseType, baseData, err := readObject(commonDir, hex.EncodeToString(ref[:]), depth+1)
		if err != nil {
			return 0, nil, err
		}
		out, err := applyDelta(baseData, delta)
		return baseType, out, err
	}
	return 0, nil, fmt.Errorf("unsupported pack object type %d", typ)
}

func inflate(r io.Reader) ([]byte, error) {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// applyDelta reconstructs an object from its base and a git delta buffer.
func applyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	readVarint := func() (uint64, error) {
		var v uint64
		var shift uint
		for {
			if pos >= len(delta) {
				return 0, errors.New("truncated delta varint")
			}
			b := delta[pos]
			pos++
			v |= uint64(b&0x7f) << shift
			if b&0x80 == 0 {
				return v, nil
			}
			shift += 7
		}
	}
	if _, err := readVarint(); err != nil { // base size
		return nil, err
	}
	outSize, err := readVarint() // result size
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, outSize)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		if op&0x80 != 0 {
			var cpOff, cpSize uint64
			for i := uint(0); i < 4; i++ {
				if op&(1<<i) != 0 {
					if pos >= len(delta) {
						return nil, errors.New("truncated delta copy")
					}
					cpOff |= uint64(delta[pos]) << (8 * i)
					pos++
				}
			}
			for i := uint(0); i < 3; i++ {
				if op&(0x10<<i) != 0 {
					if pos >= len(delta) {
						return nil, errors.New("truncated delta copy")
					}
					cpSize |= uint64(delta[pos]) << (8 * i)
					pos++
				}
			}
			if cpSize == 0 {
				cpSize = 0x10000
			}
			if cpOff+cpSize > uint64(len(base)) {
				return nil, errors.New("delta copy out of range")
			}
			out = append(out, base[cpOff:cpOff+cpSize]...)
		} else if op != 0 {
			if pos+int(op) > len(delta) {
				return nil, errors.New("truncated delta insert")
			}
			out = append(out, delta[pos:pos+int(op)]...)
			pos += int(op)
		} else {
			return nil, errors.New("invalid delta opcode 0")
		}
	}
	if uint64(len(out)) != outSize {
		return nil, errors.New("delta result size mismatch")
	}
	return out, nil
}
