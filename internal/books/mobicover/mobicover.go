// Package mobicover extracts the cover image embedded in MOBI and AZW3 books.
package mobicover

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoCover reports that the file is not a MOBI/AZW3 book or carries no
// embedded cover. Callers must not invent a cover in that case.
var ErrNoCover = errors.New("mobicover: no embedded cover")

const (
	pdbTypeCreatorOffset  = 60
	pdbRecordCountOffset  = 76
	pdbRecordOffsetStart  = 78
	pdbHeaderSize         = 68
	mobiHeaderOffset      = 16
	mobiFirstImageOffset  = 0x6C
	exthHeaderSize        = 12
	exthCoverOffsetRecord = 201
	exthHeaderMagic       = "EXTH"
	mobiMagic             = "MOBI"
	pdbMagic              = "BOOKMOBI"
)

// FromFile returns the embedded cover of the MOBI/AZW3 file at path.
func FromFile(path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read book file: %w", err)
	}
	return Parse(data)
}

// Parse returns the embedded cover from the raw bytes of a MOBI/AZW3 file.
func Parse(data []byte) ([]byte, error) {
	records, ok := recordOffsets(data)
	if !ok {
		return nil, ErrNoCover
	}
	zero, ok := record(data, records, 0)
	if !ok {
		return nil, ErrNoCover
	}
	if len(zero) < mobiHeaderOffset+4 || string(zero[mobiHeaderOffset:mobiHeaderOffset+4]) != mobiMagic {
		return nil, ErrNoCover
	}
	coverOffset, ok := exthCoverOffset(zero)
	if !ok {
		return nil, ErrNoCover
	}
	firstImage, ok := uint32At(zero, mobiFirstImageOffset)
	if !ok {
		return nil, ErrNoCover
	}
	coverRecord := int(firstImage) + int(coverOffset)
	payload, ok := record(data, records, coverRecord)
	if !ok || !isImage(payload) {
		return nil, ErrNoCover
	}
	return payload, nil
}

func recordOffsets(data []byte) ([]uint32, bool) {
	if len(data) < pdbHeaderSize || string(data[pdbTypeCreatorOffset:pdbHeaderSize]) != pdbMagic {
		return nil, false
	}
	count := int(binary.BigEndian.Uint16(data[pdbRecordCountOffset:]))
	if count < 3 || len(data) < pdbRecordOffsetStart+8*count {
		return nil, false
	}
	offsets := make([]uint32, count)
	for index := range offsets {
		offsets[index] = binary.BigEndian.Uint32(data[pdbRecordOffsetStart+8*index:])
	}
	return offsets, true
}

func record(data []byte, offsets []uint32, index int) ([]byte, bool) {
	if index < 0 || index >= len(offsets) {
		return nil, false
	}
	start := int(offsets[index])
	if start < 0 || start > len(data) {
		return nil, false
	}
	end := len(data)
	if index+1 < len(offsets) {
		next := int(offsets[index+1])
		if next < start || next > len(data) {
			return nil, false
		}
		end = next
	}
	return data[start:end], true
}

func exthCoverOffset(zero []byte) (uint32, bool) {
	headerLength, ok := uint32At(zero, mobiHeaderOffset+4)
	if !ok {
		return 0, false
	}
	start := mobiHeaderOffset + int(headerLength)
	if start < 0 || start+exthHeaderSize > len(zero) || string(zero[start:start+4]) != exthHeaderMagic {
		return 0, false
	}
	count, ok := uint32At(zero, start+8)
	if !ok {
		return 0, false
	}
	position := start + exthHeaderSize
	for range count {
		recordType, ok := uint32At(zero, position)
		if !ok {
			return 0, false
		}
		length, ok := uint32At(zero, position+4)
		if !ok || length < 8 {
			return 0, false
		}
		if recordType == exthCoverOffsetRecord {
			return uint32At(zero, position+8)
		}
		position += int(length)
	}
	return 0, false
}

func uint32At(data []byte, offset int) (uint32, bool) {
	if offset < 0 || offset+4 > len(data) {
		return 0, false
	}
	return binary.BigEndian.Uint32(data[offset:]), true
}

func isImage(data []byte) bool {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return true
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return true
	case len(data) >= 4 && string(data[:4]) == "GIF8":
		return true
	default:
		return false
	}
}
