package domain

import "time"

// Event types published through the outbox (docs/08 §1.1). The structs are also the JSON payloads.

// ShopRegistered is published when a seller signs up; catalog keeps a projection of it.
type ShopRegistered struct {
	ShopID string `json:"shopId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

func (e ShopRegistered) EventType() string   { return "auth.shop.registered" }
func (e ShopRegistered) AggregateID() string { return e.ShopID }

// UserRegistered is published when a buyer verifies their email.
type UserRegistered struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

func (e UserRegistered) EventType() string   { return "auth.user.registered" }
func (e UserRegistered) AggregateID() string { return e.UserID }

// EmailVerificationRequested asks notification-service to mail the verification link.
type EmailVerificationRequested struct {
	OtpID     string    `json:"otpId"`
	Email     string    `json:"email"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (e EmailVerificationRequested) EventType() string   { return "auth.email-verification.requested" }
func (e EmailVerificationRequested) AggregateID() string { return e.OtpID }
