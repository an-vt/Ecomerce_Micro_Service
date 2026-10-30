package domain

import (
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Codes of the upload rules.
const (
	CodeUnsupportedMedia = "UNSUPPORTED_MEDIA_TYPE"
	CodeFileTooLarge     = "FILE_TOO_LARGE"
	CodeMediaDisabled    = "MEDIA_NOT_CONFIGURED"
)

// imageExtensions are the only content types accepted, each with the extension the object gets. The extension comes from
// the declared type, never from a file name the client chose.
var imageExtensions = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// MediaStorage is the object store (a port; the S3 adapter implements it).
type MediaStorage interface {
	// PresignPut returns what a client needs to upload one object without any credential of ours.
	PresignPut(key, contentType string, size int64, ttl time.Duration) (Upload, error)
}

// Upload is a ready-to-use presigned upload.
type Upload struct {
	URL       string
	Method    string
	Headers   map[string]string // must be sent exactly as given
	PublicURL string
	Key       string
	ExpiresAt time.Time
}

// MediaPolicy decides what may be uploaded.
type MediaPolicy struct {
	MaxBytes int64
	TTL      time.Duration
}

// ValidateImage checks the declared type and size. Declared, not inspected: the store never sees the bytes until the
// client sends them, and the signature pins the type and the exact size so the client cannot send something else.
func (p MediaPolicy) ValidateImage(contentType string, size int64) (ext string, err error) {
	ext, ok := imageExtensions[contentType]
	if !ok {
		return "", errs.InvalidArgument(CodeUnsupportedMedia, "only image/jpeg, image/png, image/webp and image/gif can be uploaded")
	}
	if size <= 0 {
		return "", errs.Validation("size must be the exact number of bytes of the file")
	}
	if size > p.MaxBytes {
		return "", errs.InvalidArgument(CodeFileTooLarge, "the image is larger than the limit")
	}
	return ext, nil
}

// ObjectKey is where a shop's image goes: scoped by shop, named by a random id. Nobody can guess another object's key or
// overwrite one by choosing a name.
func ObjectKey(shopID, id, ext string) string { return "products/" + shopID + "/" + id + "." + ext }
