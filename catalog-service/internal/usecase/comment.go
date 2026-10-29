package usecase

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/pagination"
)

// CreateCommentInput is a new comment or reply.
type CreateCommentInput struct {
	ProductID, Content, ParentCommentID string
}

// CreateComment adds a comment to a product (monolith CommentService.createComment).
type CreateComment struct{ d *Deps }

// Execute checks that the product (and the parent comment, for a reply) exist, then inserts into the nested set.
func (u CreateComment) Execute(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
	caller, err := anyCaller(ctx)
	if err != nil {
		return nil, err
	}
	c, err := domain.NewComment(u.d.IDs.New(), in.ProductID, caller.ID, in.Content, in.ParentCommentID, u.d.Clock.Now())
	if err != nil {
		return nil, err
	}
	if _, err := u.d.Products.FindByID(ctx, in.ProductID); err != nil {
		return nil, err
	}
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if in.ParentCommentID == "" {
			return u.d.Comments.AddRoot(ctx, c)
		}
		parent, err := u.d.Comments.FindByID(ctx, in.ParentCommentID)
		if err != nil {
			return err
		}
		if parent.ProductID != in.ProductID {
			return domain.ErrCommentNotFound()
		}
		return u.d.Comments.AddReply(ctx, parent, c)
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// DeleteComment removes a comment and its replies.
type DeleteComment struct{ d *Deps }

// Execute lets the author (or an administrator) delete a thread; everything below it goes too.
func (u DeleteComment) Execute(ctx context.Context, commentID, productID string) error {
	caller, err := anyCaller(ctx)
	if err != nil {
		return err
	}
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		c, err := u.d.Comments.FindByID(ctx, commentID)
		if err != nil {
			return err
		}
		if c.ProductID != productID {
			return domain.ErrCommentNotFound()
		}
		if c.UserID != caller.ID && !caller.HasRole("admin") {
			return errs.PermissionDenied(domain.CodeForbidden, "you can only delete your own comments")
		}
		return u.d.Comments.DeleteSubtree(ctx, c)
	})
}

// ListComments lists the direct replies of a comment (or the top level).
type ListComments struct{ d *Deps }

// Execute pages through one level of the tree.
func (u ListComments) Execute(ctx context.Context, productID, parentID string, limit, skip int) ([]*domain.Comment, error) {
	if productID == "" {
		return nil, errs.Validation("product id is required")
	}
	if skip < 0 {
		skip = 0
	}
	return u.d.Comments.List(ctx, productID, parentID, pagination.New(1, limit).Limit, skip)
}
