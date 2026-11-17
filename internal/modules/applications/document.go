package applications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const MaxDocumentBytes int64 = 10 << 20

var ErrDocument = errors.New("invalid application document")

type DocumentStore interface {
	Put(context.Context, string, []byte) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Keys(context.Context) ([]string, error)
}

// validateDocument preserves the submitted bytes. Images are fully decoded after
// allocation bounds; PDFs receive bounded header/trailer/xref envelope checks.
// This is format validation, not a PDF interpreter or malware scanner. All files
// are delivered as attachments from an authenticated, non-cacheable handler.
func validateDocument(input io.Reader, filename string) ([]byte, string, string, error) {
	data, err := io.ReadAll(io.LimitReader(input, MaxDocumentBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(data) == 0 || int64(len(data)) > MaxDocumentBytes {
		return nil, "", "", ErrDocument
	}
	ext := strings.ToLower(filepath.Ext(filename))
	media := ""
	switch ext {
	case ".jpg", ".jpeg", ".png":
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		expected := "jpeg"
		if ext == ".png" {
			expected = "png"
		}
		if err != nil || format != expected || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 12000000 {
			return nil, "", "", ErrDocument
		}
		decoded, f, err := image.Decode(bytes.NewReader(data))
		if err != nil || f != expected || decoded.Bounds().Dx() != cfg.Width || decoded.Bounds().Dy() != cfg.Height {
			return nil, "", "", ErrDocument
		}
		media = "image/jpeg"
		ext = ".jpg"
		if expected == "png" {
			media = "image/png"
			ext = ".png"
		}
	case ".pdf":
		if !regexp.MustCompile(`^%PDF-(1\.[0-7]|2\.0)[\r\n]`).Match(data) {
			return nil, "", "", ErrDocument
		}
		trimmed := bytes.TrimSpace(data)
		if !bytes.HasSuffix(trimmed, []byte("%%EOF")) {
			return nil, "", "", ErrDocument
		}
		start := bytes.LastIndex(trimmed, []byte("startxref"))
		if start < 0 {
			return nil, "", "", ErrDocument
		}
		parts := bytes.Fields(trimmed[start:])
		if len(parts) != 3 || string(parts[0]) != "startxref" || string(parts[2]) != "%%EOF" {
			return nil, "", "", ErrDocument
		}
		offset, err := strconv.ParseInt(string(parts[1]), 10, 64)
		if err != nil || offset < 9 || offset >= int64(start) {
			return nil, "", "", ErrDocument
		}
		xref := trimmed[offset:]
		if !bytes.HasPrefix(xref, []byte("xref")) && !regexp.MustCompile(`^\d+\s+\d+\s+obj\b`).Match(xref) {
			return nil, "", "", ErrDocument
		}
		media = "application/pdf"
	default:
		return nil, "", "", ErrDocument
	}
	return data, media, ext, nil
}
func documentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
