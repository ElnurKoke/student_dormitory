package inventory

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

const (
	MaxPhotoBytes  int64 = 5 << 20
	MaxPhotoSide         = 4096
	MaxPhotoPixels       = 12000000
)

var ErrPhoto = errors.New("invalid photo")

// PhotoStore writes durable new objects; Open returns a reader independent of
// subsequent Delete. Keys enumerates this application's photo namespace only.
// Put must never replace an existing key. Delete is idempotent.
type PhotoStore interface {
	Put(context.Context, string, []byte) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Keys(context.Context) ([]string, error)
}
type Photo struct {
	ID, DormitoryID, RoomID, StorageKey, MediaType, Description, CreatedAt string
	ByteSize                                                               int64
	Width, Height                                                          int
	Position                                                               int64
	Primary                                                                bool
	Version                                                                int64
}
type PhotoDraft struct {
	Description string
	Position    int64
	Primary     bool
}

func validatePhotoDraft(d PhotoDraft) (PhotoDraft, error) {
	d.Description = strings.TrimSpace(d.Description)
	if !utf8.ValidString(d.Description) || utf8.RuneCountInString(d.Description) > 1000 || strings.ContainsRune(d.Description, 0) || d.Position < 0 || d.Position > 1000000 {
		return d, ErrPhoto
	}
	return d, nil
}

// DecodeConfig bounds allocations before full decoding. Re-encoding only decoded
// pixels discards EXIF, ICC, XMP, comments and trailing/polyglot payloads. WebP is
// normalized to PNG (including transparency); JPEG remains JPEG.
func normalizePhoto(input io.Reader) ([]byte, Photo, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxPhotoBytes+1))
	if err != nil {
		return nil, Photo{}, err
	}
	if len(raw) == 0 || int64(len(raw)) > MaxPhotoBytes {
		return nil, Photo{}, ErrPhoto
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxPhotoSide || cfg.Height > MaxPhotoSide || int64(cfg.Width)*int64(cfg.Height) > MaxPhotoPixels {
		return nil, Photo{}, ErrPhoto
	}
	pixels, decodedFormat, err := image.Decode(bytes.NewReader(raw))
	if err != nil || decodedFormat != format || pixels.Bounds().Dx() != cfg.Width || pixels.Bounds().Dy() != cfg.Height {
		return nil, Photo{}, ErrPhoto
	}
	var out bytes.Buffer
	mediaType := "image/png"
	if format == "jpeg" {
		mediaType = "image/jpeg"
		err = jpeg.Encode(&out, pixels, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&out, pixels)
	}
	if err != nil {
		return nil, Photo{}, err
	}
	// The same limit applies to stored output; noisy lossless images may grow.
	if int64(out.Len()) > MaxPhotoBytes {
		return nil, Photo{}, ErrPhoto
	}
	return out.Bytes(), Photo{MediaType: mediaType, ByteSize: int64(out.Len()), Width: cfg.Width, Height: cfg.Height}, nil
}
