package domain

import (
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Comment is a node of a product's comment tree, stored as a nested set: every comment has a left and right value,
// and its descendants are exactly the comments whose values lie inside that interval (monolith comment.service).
type Comment struct {
	ID        string
	ProductID string
	UserID    string
	Content   string
	Left      int64
	Right     int64
	ParentID  string
	IsDeleted bool
	CreatedAt time.Time
}

// MaxCommentLength bounds a comment.
const MaxCommentLength = 2000

// NewComment validates a comment; the repository assigns Left / Right when it inserts it.
func NewComment(id, productID, userID, content, parentID string, now time.Time) (*Comment, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > MaxCommentLength {
		return nil, errs.Validation("comment content is required and must be at most 2000 characters")
	}
	if productID == "" || userID == "" {
		return nil, errs.Validation("product and user are required")
	}
	return &Comment{ID: id, ProductID: productID, UserID: userID, Content: content, ParentID: parentID, CreatedAt: now}, nil
}

// Placement is where a new comment goes in the tree and which existing nodes must move to make room.
type Placement struct {
	Left, Right int64
	// ShiftRightsFrom: comments of the same product whose Right >= this value get Right += 2.
	// ShiftLeftsAfter: comments whose Left > this value get Left += 2. Zero values mean "no shifting" (a new root).
	ShiftRightsFrom int64
	ShiftLeftsAfter int64
	Shift           bool
}

// PlanRoot places a new top-level comment after everything (maxRight = highest Right of the product, 0 if none).
func PlanRoot(maxRight int64) Placement { return Placement{Left: maxRight + 1, Right: maxRight + 2} }

// PlanReply places a reply as the last child of a parent whose Right is parentRight: the parent and everything to
// the right of it shift by 2.
func PlanReply(parentRight int64) Placement {
	return Placement{Left: parentRight, Right: parentRight + 1, ShiftRightsFrom: parentRight, ShiftLeftsAfter: parentRight, Shift: true}
}

// Removal describes deleting a comment with its whole subtree.
type Removal struct {
	// Delete every comment of the product with Left >= Left and Right <= Right.
	Left, Right int64
	// Width is how far the nodes after the removed interval move back.
	Width int64
}

// PlanDelete computes the removal of c's subtree: the comments to the right shift left by Width.
func PlanDelete(c Comment) Removal {
	return Removal{Left: c.Left, Right: c.Right, Width: c.Right - c.Left + 1}
}
