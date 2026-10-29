package usecase

// Set groups the use cases; the transports receive it whole.
type Set struct {
	SendEmail             SendEmail
	SendVerificationEmail SendVerificationEmail
	SendWelcomeEmail      SendWelcomeEmail
	NotifyProductCreated  NotifyProductCreated
	NotifyDiscountCreated NotifyDiscountCreated
	NotifyOrderConfirmed  NotifyOrderConfirmed
	NotifyOrderCancelled  NotifyOrderCancelled
	ListNotifications     ListNotifications
	CreateTemplate        CreateTemplate
	EnsureTemplates       EnsureTemplates
}

// NewSet wires every use case to the same dependencies.
func NewSet(d *Deps) *Set {
	return &Set{SendEmail: SendEmail{d}, SendVerificationEmail: SendVerificationEmail{d}, SendWelcomeEmail: SendWelcomeEmail{d},
		NotifyProductCreated: NotifyProductCreated{d}, NotifyDiscountCreated: NotifyDiscountCreated{d}, NotifyOrderConfirmed: NotifyOrderConfirmed{d},
		NotifyOrderCancelled: NotifyOrderCancelled{d}, ListNotifications: ListNotifications{d}, CreateTemplate: CreateTemplate{d}, EnsureTemplates: EnsureTemplates{d}}
}
