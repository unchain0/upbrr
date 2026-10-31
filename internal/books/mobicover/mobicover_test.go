package mobicover_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/autobrr/upbrr/internal/books/mobicover"
)

var jpegCover = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, append(make([]byte, 64), 0xFF, 0xD9)...)

func exth(coverOffset *uint32) []byte {
	if coverOffset == nil {
		return concat([]byte("EXTH"), be32(12), be32(0))
	}
	return concat(
		[]byte("EXTH"),
		be32(24),
		be32(1),
		be32(201),
		be32(12),
		be32(*coverOffset),
	)
}

func be32(value uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, value)
	return out
}

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func recordZero(firstImage uint32, coverOffset *uint32) []byte {
	header := make([]byte, 16+232)
	copy(header[16:20], "MOBI")
	binary.BigEndian.PutUint32(header[20:24], 232)
	binary.BigEndian.PutUint32(header[0x6C:0x70], firstImage)
	return concat(header, exth(coverOffset))
}

func book(records ...[]byte) []byte {
	size := 78 + 8*len(records)
	for _, record := range records {
		size += len(record)
	}
	out := make([]byte, 0, size)
	header := make([]byte, 78+8*len(records))
	copy(header[60:68], "BOOKMOBI")
	binary.BigEndian.PutUint16(header[76:78], uint16(len(records)))
	offset := len(header)
	for index, record := range records {
		binary.BigEndian.PutUint32(header[78+8*index:], uint32(offset))
		offset += len(record)
	}
	out = append(out, header...)
	for _, record := range records {
		out = append(out, record...)
	}
	return out
}

func TestParseReturnsTheEmbeddedCover(t *testing.T) {
	t.Parallel()
	offset := uint32(0)
	data := book(recordZero(2, &offset), []byte("text record"), jpegCover)

	cover, err := mobicover.Parse(data)

	if err != nil {
		t.Fatalf("Parse returned %v", err)
	}
	if !bytes.Equal(cover, jpegCover) {
		t.Fatalf("cover mismatch: %x", cover)
	}
}

func TestParseWithoutCoverMetadataReportsNoCover(t *testing.T) {
	t.Parallel()
	data := book(recordZero(2, nil), []byte("text record"), jpegCover)

	if _, err := mobicover.Parse(data); !errors.Is(err, mobicover.ErrNoCover) {
		t.Fatalf("expected ErrNoCover, got %v", err)
	}
}

func TestParseRejectsACoverOffsetThatPointsAtText(t *testing.T) {
	t.Parallel()
	offset := uint32(0)
	data := book(recordZero(1, &offset), []byte("text record"), jpegCover)

	if _, err := mobicover.Parse(data); !errors.Is(err, mobicover.ErrNoCover) {
		t.Fatalf("expected ErrNoCover, got %v", err)
	}
}

func TestParseRejectsFilesThatAreNotBooks(t *testing.T) {
	t.Parallel()

	if _, err := mobicover.Parse([]byte("not a mobi file at all")); !errors.Is(err, mobicover.ErrNoCover) {
		t.Fatalf("expected ErrNoCover, got %v", err)
	}
}

func TestFromFileReadsTheCover(t *testing.T) {
	t.Parallel()
	offset := uint32(0)
	path := filepath.Join(t.TempDir(), "book.azw3")
	if err := os.WriteFile(path, book(recordZero(2, &offset), []byte("text"), jpegCover), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cover, err := mobicover.FromFile(path)

	if err != nil {
		t.Fatalf("FromFile returned %v", err)
	}
	if !bytes.Equal(cover, jpegCover) {
		t.Fatalf("cover mismatch: %x", cover)
	}
}

func TestFromFileReportsAMissingFile(t *testing.T) {
	t.Parallel()

	_, err := mobicover.FromFile(filepath.Join(t.TempDir(), "missing.mobi"))

	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, mobicover.ErrNoCover) {
		t.Fatalf("missing file must not report ErrNoCover: %v", err)
	}
}
