package domain

import (
	"encoding/json"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Type of a notification (docs/04 §8).
type Type string

const (
	TypeOrderConfirmed Type = "ORDER-001" // the buyer's order was confirmed
	TypeOrderCancelled Type = "ORDER-002" // the buyer's order was cancelled
	TypePromotion      Type = "PROMOTION-001"
	TypeNewProduct     Type = "SHOP-001"
)

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	switch t {
	case TypeOrderConfirmed, TypeOrderCancelled, TypePromotion, TypeNewProduct:
		return true
	}
	return false
}

// Broadcast is the receiver of a notification meant for everybody.
const Broadcast = "*"

// Notification is something a person should see.
type Notification struct {
	ID         string
	Type       Type
	SenderID   string
	ReceiverID string // an account id, or Broadcast
	Content    string
	Options    map[string]any
	IsRead     bool
	DedupeKey  string // the message that caused it: a redelivered message cannot create a second notification
	CreatedAt  time.Time
}

// NewNotification validates a notification.
func NewNotification(id string, typ Type, senderID, receiverID, content string, options map[string]any, dedupeKey string, now time.Time) (*Notification, error) {
	switch {
	case id == "" || dedupeKey == "":
		return nil, errs.Validation("a notification needs an id and the key of the message that caused it")
	case !typ.Valid():
		return nil, errs.Validation("unknown notification type " + string(typ))
	case receiverID == "" || content == "":
		return nil, errs.Validation("a notification needs a receiver and a content")
	}
	if options == nil {
		options = map[string]any{}
	}
	return &Notification{ID: id, Type: typ, SenderID: senderID, ReceiverID: receiverID, Content: content, Options: options, DedupeKey: dedupeKey, CreatedAt: now}, nil
}

// OptionsJSON is the options as a JSON object.
func (n *Notification) OptionsJSON() string {
	b, err := json.Marshal(n.Options)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ListFilter selects notifications for one account (its own and the broadcasts).
type ListFilter struct {
	ReceiverID string
	Type       Type  // empty: every type
	IsRead     *bool // nil: either
}
