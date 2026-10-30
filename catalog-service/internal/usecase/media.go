package usecase

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// CreateUploadURL lets a shop upload a product image straight to the object store.
type CreateUploadURL struct{ d *Deps }

// Execute checks the declared type and size and returns a presigned PUT. The image never passes through this service
// (the monolith received the bytes itself, through an unauthenticated route). The shop uses the returned public URL as the
// thumb of a product. When no object store is configured the feature is off and the answer says so.
func (u CreateUploadURL) Execute(ctx context.Context, contentType string, size int64) (domain.Upload, error) {
	me, err := shopCaller(ctx)
	if err != nil {
		return domain.Upload{}, err
	}
	if u.d.Media == nil {
		return domain.Upload{}, errs.FailedPrecondition(domain.CodeMediaDisabled, "image upload is not enabled on this deployment")
	}
	ext, err := u.d.MediaPolicy.ValidateImage(contentType, size)
	if err != nil {
		return domain.Upload{}, err
	}
	return u.d.Media.PresignPut(domain.ObjectKey(me.ID, u.d.IDs.New(), ext), contentType, size, u.d.MediaPolicy.TTL)
}
