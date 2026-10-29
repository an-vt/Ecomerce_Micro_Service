package usecase

// Set groups the use cases; transport handlers receive it whole.
type Set struct {
	SignUpShop               SignUpShop
	Login                    *Login
	Refresh                  Refresh
	Logout                   Logout
	RequestEmailVerification RequestEmailVerification
	VerifyEmail              VerifyEmail
	SetPassword              SetPassword
	GetAccount               GetAccount
	ValidateApiKey           ValidateApiKey
	GetJwks                  GetJwks

	CreateRole     CreateRole
	ListRoles      ListRoles
	CreateResource CreateResource
	ListResources  ListResources
	GetGrants      GetGrants
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{
		SignUpShop: SignUpShop{d}, Login: &Login{d: d}, Refresh: Refresh{d}, Logout: Logout{d},
		RequestEmailVerification: RequestEmailVerification{d}, VerifyEmail: VerifyEmail{d},
		SetPassword: SetPassword{d}, GetAccount: GetAccount{d}, ValidateApiKey: ValidateApiKey{d}, GetJwks: GetJwks{d},
		CreateRole: CreateRole{d}, ListRoles: ListRoles{d}, CreateResource: CreateResource{d},
		ListResources: ListResources{d}, GetGrants: GetGrants{d},
	}
}
