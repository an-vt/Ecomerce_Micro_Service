package usecase

// Set groups the use cases; transport handlers receive it whole.
type Set struct {
	CreateInventory    CreateInventory
	SyncStock          SyncStock
	AddStock           AddStock
	GetStock           GetStock
	CheckStock         CheckStock
	ReserveStock       ReserveStock
	ReleaseStock       ReleaseStock
	CommitStock        CommitStock
	ExpireReservations ExpireReservations
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{CreateInventory: CreateInventory{d}, SyncStock: SyncStock{d}, AddStock: AddStock{d}, GetStock: GetStock{d},
		CheckStock: CheckStock{d}, ReserveStock: ReserveStock{d}, ReleaseStock: ReleaseStock{d}, CommitStock: CommitStock{d},
		ExpireReservations: ExpireReservations{d}}
}
