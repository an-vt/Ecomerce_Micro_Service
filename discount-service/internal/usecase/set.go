package usecase

// Set groups the use cases; transport handlers receive it whole.
type Set struct {
	CreateDiscount      CreateDiscount
	UpdateDiscount      UpdateDiscount
	ListShopDiscounts   ListShopDiscounts
	ListProductsForCode ListProductsForCode
	CalculateDiscount   CalculateDiscount
	ApplyDiscounts      ApplyDiscounts
	ReleaseDiscounts    ReleaseDiscounts
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{CreateDiscount: CreateDiscount{d}, UpdateDiscount: UpdateDiscount{d}, ListShopDiscounts: ListShopDiscounts{d},
		ListProductsForCode: ListProductsForCode{d}, CalculateDiscount: CalculateDiscount{d}, ApplyDiscounts: ApplyDiscounts{d},
		ReleaseDiscounts: ReleaseDiscounts{d}}
}
