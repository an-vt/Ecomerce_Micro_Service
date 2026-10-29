package domain

// ProductCreated is published when a shop adds a product; inventory creates its stock row and notification tells
// followers (docs/08 §1.1).
type ProductCreated struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId"`
	Name      string `json:"name"`
	Price     int64  `json:"price"`
	Quantity  int64  `json:"quantity"`
	Type      string `json:"type"`
}

func (e ProductCreated) EventType() string   { return "catalog.product.created" }
func (e ProductCreated) AggregateID() string { return e.ProductID }

// ProductUpdated is published after an update; Quantity is set only when the stock level was part of it.
type ProductUpdated struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId"`
	Name      string `json:"name"`
	Price     int64  `json:"price"`
	Quantity  *int64 `json:"quantity,omitempty"`
}

func (e ProductUpdated) EventType() string   { return "catalog.product.updated" }
func (e ProductUpdated) AggregateID() string { return e.ProductID }
