package kafkax

// Topic names (docs/08-messaging.md §1). Every consumed topic T also has T.dlq.
const (
	// domain events
	TopicShopRegistered    = "auth.shop.registered"
	TopicUserRegistered    = "auth.user.registered"
	TopicEmailVerification = "auth.email-verification.requested"
	TopicProductCreated    = "catalog.product.created"
	TopicProductUpdated    = "catalog.product.updated"
	TopicDiscountCreated   = "discount.discount.created"
	TopicOrderConfirmed    = "order.order.confirmed"
	TopicOrderCancelled    = "order.order.cancelled"
	// saga commands and replies
	TopicInventoryCommands = "inventory.commands"
	TopicDiscountCommands  = "discount.commands"
	TopicPaymentCommands   = "payment.commands"
	TopicSagaReplies       = "order.saga.replies"
)

// DLQ returns the dead-letter topic of t.
func DLQ(t string) string { return t + ".dlq" }
