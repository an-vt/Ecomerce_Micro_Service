package usecase

// Set groups the use cases; transport handlers receive it whole.
type Set struct {
	ReviewCheckout    ReviewCheckout
	PlaceOrder        PlaceOrder
	GetOrder          GetOrder
	ListOrders        ListOrders
	HandleSagaReply   HandleSagaReply
	SweepSagaTimeouts SweepSagaTimeouts
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{ReviewCheckout: ReviewCheckout{d}, PlaceOrder: PlaceOrder{d}, GetOrder: GetOrder{d}, ListOrders: ListOrders{d},
		HandleSagaReply: HandleSagaReply{d}, SweepSagaTimeouts: SweepSagaTimeouts{d}}
}
