// Package saga defines the messages of the checkout saga (docs/06-checkout-saga.md §3): the commands order-service
// sends to inventory, discount and payment, and the replies they send back. Both sides import these types, so the
// wire contract has one definition. Every message carries the order id; commands and replies use it as the Kafka key,
// which keeps all messages of one order in order on one partition.
package saga

import (
	"time"

	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/kafkax"
)

// Topics.
const (
	TopicInventoryCommands = kafkax.TopicInventoryCommands
	TopicDiscountCommands  = kafkax.TopicDiscountCommands
	TopicPaymentCommands   = kafkax.TopicPaymentCommands
	TopicReplies           = kafkax.TopicSagaReplies
)

// Command and reply type names (the envelope "type").
const (
	CmdReserveStock    = "ReserveStock"
	CmdReleaseStock    = "ReleaseStock"
	CmdCommitStock     = "CommitStock"
	CmdApplyDiscounts  = "ApplyDiscounts"
	CmdReleaseDiscount = "ReleaseDiscounts"
	CmdProcessPayment  = "ProcessPayment"
	CmdRefundPayment   = "RefundPayment"

	ReplyStockReserved          = "StockReserved"
	ReplyStockReservationFailed = "StockReservationFailed"
	ReplyStockReleased          = "StockReleased"
	ReplyStockCommitted         = "StockCommitted"
	ReplyDiscountsApplied       = "DiscountsApplied"
	ReplyDiscountsRejected      = "DiscountsRejected"
	ReplyDiscountsReleased      = "DiscountsReleased"
	ReplyPaymentCompleted       = "PaymentCompleted"
	ReplyPaymentFailed          = "PaymentFailed"
	ReplyPaymentRefunded        = "PaymentRefunded"
)

// Failure reasons carried by the *Failed / *Rejected replies and stored as the order's cancel reason.
const (
	ReasonInsufficientStock   = "INSUFFICIENT_STOCK"
	ReasonUnknownProduct      = "UNKNOWN_PRODUCT"
	ReasonCardDeclined        = "CARD_DECLINED"
	ReasonProviderUnavailable = "PROVIDER_UNAVAILABLE"
	ReasonDiscountRejected    = "DISCOUNT_REJECTED"
	ReasonStepTimeout         = "STEP_TIMEOUT"
)

// Item is a product line of an order.
type Item struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId,omitempty"`
	Quantity  int64  `json:"quantity"`
	Price     int64  `json:"price,omitempty"`
}

// ---- commands ---------------------------------------------------------------------------------------------------

// ReserveStock asks inventory to hold stock for an order (all items or none).
type ReserveStock struct {
	OrderID string `json:"orderId"`
	Items   []Item `json:"items"`
}

// ReleaseStock gives the held stock back (compensation). Harmless when nothing is held.
type ReleaseStock struct {
	OrderID string `json:"orderId"`
}

// CommitStock turns the hold into a sale after the payment succeeded.
type CommitStock struct {
	OrderID string `json:"orderId"`
}

// DiscountRequest is one discount code of a shop.
type DiscountRequest struct {
	ShopID string `json:"shopId"`
	Code   string `json:"code"`
}

// ApplyDiscounts asks discount-service to validate and record the codes of an order.
type ApplyDiscounts struct {
	OrderID   string            `json:"orderId"`
	UserID    string            `json:"userId"`
	Discounts []DiscountRequest `json:"discounts"`
	Items     []Item            `json:"items"`
}

// ReleaseDiscounts undoes the usage of an order's codes (compensation).
type ReleaseDiscounts struct {
	OrderID string `json:"orderId"`
}

// ProcessPayment asks payment-service to charge the order.
type ProcessPayment struct {
	OrderID string `json:"orderId"`
	UserID  string `json:"userId"`
	Amount  int64  `json:"amount"`
	Method  string `json:"method"`
	Token   string `json:"token"`
}

// RefundPayment undoes a charge (compensation).
type RefundPayment struct {
	OrderID string `json:"orderId"`
}

// ---- replies ----------------------------------------------------------------------------------------------------

// StockReserved: the hold succeeded.
type StockReserved struct {
	OrderID string `json:"orderId"`
}

// StockReservationFailed: nothing was held.
type StockReservationFailed struct {
	OrderID   string `json:"orderId"`
	Reason    string `json:"reason"`
	ProductID string `json:"productId,omitempty"`
}

// StockReleased: the hold is gone (or never existed).
type StockReleased struct {
	OrderID string `json:"orderId"`
}

// StockCommitted: the hold became a sale.
type StockCommitted struct {
	OrderID string `json:"orderId"`
}

// DiscountsApplied: the codes were valid and recorded; TotalDiscount is what to subtract from the order.
type DiscountsApplied struct {
	OrderID       string `json:"orderId"`
	TotalDiscount int64  `json:"totalDiscount"`
}

// DiscountsRejected: a code could not be used; nothing was recorded.
type DiscountsRejected struct {
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
	Code    string `json:"code,omitempty"`
}

// DiscountsReleased: the usage was undone.
type DiscountsReleased struct {
	OrderID string `json:"orderId"`
}

// PaymentCompleted: the charge succeeded.
type PaymentCompleted struct {
	OrderID   string `json:"orderId"`
	PaymentID string `json:"paymentId"`
}

// PaymentFailed: the charge failed; nothing was charged.
type PaymentFailed struct {
	OrderID string `json:"orderId"`
	Reason  string `json:"reason"`
}

// PaymentRefunded: the charge was refunded (or cancelled before it happened).
type PaymentRefunded struct {
	OrderID string `json:"orderId"`
}

// Message is implemented by every saga message so senders can build envelopes uniformly.
type Message interface {
	MessageType() string
	Order() string
}

func (ReserveStock) MessageType() string           { return CmdReserveStock }
func (m ReserveStock) Order() string               { return m.OrderID }
func (ReleaseStock) MessageType() string           { return CmdReleaseStock }
func (m ReleaseStock) Order() string               { return m.OrderID }
func (CommitStock) MessageType() string            { return CmdCommitStock }
func (m CommitStock) Order() string                { return m.OrderID }
func (ApplyDiscounts) MessageType() string         { return CmdApplyDiscounts }
func (m ApplyDiscounts) Order() string             { return m.OrderID }
func (ReleaseDiscounts) MessageType() string       { return CmdReleaseDiscount }
func (m ReleaseDiscounts) Order() string           { return m.OrderID }
func (ProcessPayment) MessageType() string         { return CmdProcessPayment }
func (m ProcessPayment) Order() string             { return m.OrderID }
func (RefundPayment) MessageType() string          { return CmdRefundPayment }
func (m RefundPayment) Order() string              { return m.OrderID }
func (StockReserved) MessageType() string          { return ReplyStockReserved }
func (m StockReserved) Order() string              { return m.OrderID }
func (StockReservationFailed) MessageType() string { return ReplyStockReservationFailed }
func (m StockReservationFailed) Order() string     { return m.OrderID }
func (StockReleased) MessageType() string          { return ReplyStockReleased }
func (m StockReleased) Order() string              { return m.OrderID }
func (StockCommitted) MessageType() string         { return ReplyStockCommitted }
func (m StockCommitted) Order() string             { return m.OrderID }
func (DiscountsApplied) MessageType() string       { return ReplyDiscountsApplied }
func (m DiscountsApplied) Order() string           { return m.OrderID }
func (DiscountsRejected) MessageType() string      { return ReplyDiscountsRejected }
func (m DiscountsRejected) Order() string          { return m.OrderID }
func (DiscountsReleased) MessageType() string      { return ReplyDiscountsReleased }
func (m DiscountsReleased) Order() string          { return m.OrderID }
func (PaymentCompleted) MessageType() string       { return ReplyPaymentCompleted }
func (m PaymentCompleted) Order() string           { return m.OrderID }
func (PaymentFailed) MessageType() string          { return ReplyPaymentFailed }
func (m PaymentFailed) Order() string              { return m.OrderID }
func (PaymentRefunded) MessageType() string        { return ReplyPaymentRefunded }
func (m PaymentRefunded) Order() string            { return m.OrderID }

// Envelope wraps m for Kafka: the order id is the key and the correlation id.
func Envelope(messageID string, at time.Time, m Message) (event.Envelope, error) {
	return event.NewEnvelope(messageID, m.MessageType(), m.Order(), m.Order(), at, m)
}
