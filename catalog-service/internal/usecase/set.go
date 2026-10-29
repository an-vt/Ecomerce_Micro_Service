package usecase

// Set groups the use cases; transport handlers receive it whole.
type Set struct {
	CreateProduct        CreateProduct
	UpdateProduct        UpdateProduct
	SetPublished         SetPublished
	GetProduct           GetProduct
	ListProducts         ListProducts
	SearchProducts       SearchProducts
	ListShopProducts     ListShopProducts
	GetProductsByIds     GetProductsByIds
	ListProductIdsByShop ListProductIdsByShop

	CreateSpu CreateSpu
	GetSpu    GetSpu
	GetSku    GetSku

	CreateComment CreateComment
	DeleteComment DeleteComment
	ListComments  ListComments

	UpsertShop UpsertShop
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{
		CreateProduct: CreateProduct{d}, UpdateProduct: UpdateProduct{d}, SetPublished: SetPublished{d}, GetProduct: GetProduct{d},
		ListProducts: ListProducts{d}, SearchProducts: SearchProducts{d}, ListShopProducts: ListShopProducts{d},
		GetProductsByIds: GetProductsByIds{d}, ListProductIdsByShop: ListProductIdsByShop{d},
		CreateSpu: CreateSpu{d}, GetSpu: GetSpu{d}, GetSku: GetSku{d},
		CreateComment: CreateComment{d}, DeleteComment: DeleteComment{d}, ListComments: ListComments{d},
		UpsertShop: UpsertShop{d},
	}
}
