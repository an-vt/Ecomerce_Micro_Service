package domain

import "github.com/an-vt/ecom/platform-go/kafkax"

// TopicCreated is where DiscountCreated is published.
const TopicCreated = kafkax.TopicDiscountCreated

// DiscountCreated is published when a shop creates a code; notification-service announces it (PROMOTION-001).
type DiscountCreated struct {
	DiscountID string `json:"discountId"`
	ShopID     string `json:"shopId"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Value      int64  `json:"value"`
}

func (e DiscountCreated) EventType() string   { return "discount.discount.created" }
func (e DiscountCreated) AggregateID() string { return e.DiscountID }
