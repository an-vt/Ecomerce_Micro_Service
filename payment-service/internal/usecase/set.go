package usecase

// Set groups the use cases; the Kafka consumer receives it whole.
type Set struct {
	ProcessPayment ProcessPayment
	RefundPayment  RefundPayment
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{ProcessPayment: ProcessPayment{d}, RefundPayment: RefundPayment{d}}
}
