package usecase

// Set groups the use cases; transport handlers receive it whole.
type Set struct {
	AddToCart    AddToCart
	UpdateCart   UpdateCart
	RemoveItem   RemoveItem
	GetCart      GetCart
	ClearOrdered ClearOrdered
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{AddToCart: AddToCart{d}, UpdateCart: UpdateCart{d}, RemoveItem: RemoveItem{d}, GetCart: GetCart{d}, ClearOrdered: ClearOrdered{d}}
}
