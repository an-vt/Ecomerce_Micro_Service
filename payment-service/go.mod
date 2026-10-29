module github.com/an-vt/ecom/payment-service

go 1.26.0

require (
	github.com/an-vt/ecom/platform-go v0.0.0
	github.com/an-vt/ecom/proto v0.0.0-00010101000000-000000000000
)

replace github.com/an-vt/ecom/platform-go => ../platform-go

replace github.com/an-vt/ecom/proto => ../proto
