package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/notification-service/internal/fake"
	"github.com/an-vt/ecom/notification-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type rig struct {
	uc   *usecase.Set
	w    *fake.World
	smtp *fake.Sender
	clk  *clock.Fixed
}

func newRig(t *testing.T) *rig {
	t.Helper()
	w := fake.NewWorld()
	s := &fake.Sender{}
	clk := clock.NewFixed(now)
	uc := usecase.NewSet(&usecase.Deps{Notifications: w.Notifications(), Templates: w.Templates(), Emails: w.Emails(), Sender: s, Clock: clk, IDs: idgen.ULID{}})
	_, err := uc.EnsureTemplates.Execute(context.Background())
	require.NoError(t, err)
	return &rig{uc: uc, w: w, smtp: s, clk: clk}
}

func as(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeUser})
}

func verification() usecase.VerificationRequested {
	return usecase.VerificationRequested{Email: "an@example.com", Link: "http://localhost:8088/v1/api/user/welcome_back?token=abc&x=1", ExpiresAt: now.Add(15 * time.Minute)}
}

// Acceptance #2 (in-process part): the verification event becomes an email with the link.
func TestVerificationEmailCarriesTheLink(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.uc.SendVerificationEmail.Execute(context.Background(), "msg-1", verification()))

	require.Equal(t, 1, r.smtp.Count())
	m := r.smtp.Sent[0]
	assert.Equal(t, "an@example.com", m.To)
	assert.Equal(t, "Confirm your email", m.Subject)
	assert.Contains(t, m.HTML, `href="http://localhost:8088/v1/api/user/welcome_back?token=abc&amp;x=1"`, "the link, HTML-escaped inside the attribute")
	assert.Contains(t, m.HTML, "Fri, 30 Oct 2026 12:15:00 UTC")
	assert.NotContains(t, m.HTML, "{{", "every placeholder replaced")
	e, ok := r.w.Email("msg-1:email-verification")
	require.True(t, ok)
	assert.Equal(t, domain.EmailSent, e.Status)
	assert.Equal(t, 1, e.Attempts)
}

// Acceptance #3: SMTP down is an error to retry; the record shows the failure; redelivery after success sends nothing new.
func TestSMTPDownIsRetriedAndANewDeliveryDoesNotDuplicate(t *testing.T) {
	r := newRig(t)
	r.smtp.Err = errors.New("dial tcp: connection refused")
	err := r.uc.SendVerificationEmail.Execute(context.Background(), "msg-1", verification())
	require.Error(t, err)
	assert.False(t, errs.IsPermanent(err), "the consumer retries with backoff and then dead-letters it")
	e, _ := r.w.Email("msg-1:email-verification")
	assert.Equal(t, domain.EmailFailed, e.Status)
	assert.Contains(t, e.LastError, "connection refused")
	assert.Equal(t, 1, e.Attempts)

	err = r.uc.SendVerificationEmail.Execute(context.Background(), "msg-1", verification())
	require.Error(t, err)
	e, _ = r.w.Email("msg-1:email-verification")
	assert.Equal(t, 2, e.Attempts, "the second attempt is on the same record")

	r.smtp.Err = nil
	require.NoError(t, r.uc.SendVerificationEmail.Execute(context.Background(), "msg-1", verification()))
	assert.Equal(t, 1, r.smtp.Count())
	for i := 0; i < 3; i++ { // the broker redelivers the message after it was handled
		require.NoError(t, r.uc.SendVerificationEmail.Execute(context.Background(), "msg-1", verification()))
	}
	assert.Equal(t, 1, r.smtp.Count(), "one email, however often the message arrives")
	e, _ = r.w.Email("msg-1:email-verification")
	assert.Equal(t, domain.EmailSent, e.Status)
	assert.Equal(t, 3, e.Attempts)
}

func TestAnExpiredLinkIsNotSent(t *testing.T) {
	r := newRig(t)
	ev := verification()
	ev.ExpiresAt = now.Add(-time.Second)
	require.NoError(t, r.uc.SendVerificationEmail.Execute(context.Background(), "msg-old", ev))
	assert.Zero(t, r.smtp.Count(), "the topic is replayed from the start on a first run: dead links are not mailed")
	_, ok := r.w.Email("msg-old:email-verification")
	assert.False(t, ok)

	ev.ExpiresAt = now
	require.NoError(t, r.uc.SendVerificationEmail.Execute(context.Background(), "msg-edge", ev))
	assert.Zero(t, r.smtp.Count(), "expiring right now is expired")
}

func TestPermanentEmailFailuresAreMarkedAndNotRetried(t *testing.T) {
	r := newRig(t)
	bad := verification()
	bad.Email = "a@b.co\r\nBcc: victim@x.y"
	err := r.uc.SendVerificationEmail.Execute(context.Background(), "m1", bad)
	assert.Equal(t, domain.CodeInvalidEmail, errs.CodeOf(err))
	assert.True(t, errs.IsPermanent(err))
	assert.Zero(t, r.smtp.Count())

	err = r.uc.SendEmail.Execute(context.Background(), usecase.EmailRequest{MessageID: "m2", To: "a@b.co", Subject: "s", Template: "no-such-template"})
	assert.Equal(t, domain.CodeTemplateNotFound, errs.CodeOf(err))
	assert.True(t, errs.IsPermanent(err))
	e, _ := r.w.Email("m2:no-such-template")
	assert.Equal(t, domain.EmailFailed, e.Status, "visible to whoever looks at the dead-letter topic")
	assert.Contains(t, e.LastError, "no-such-template")

	err = r.uc.SendEmail.Execute(context.Background(), usecase.EmailRequest{MessageID: "m3", To: "a@b.co", Subject: "s", Template: domain.TemplateWelcome})
	assert.Equal(t, domain.CodeMissingParam, errs.CodeOf(err), "a template whose value was not supplied is never sent with a literal {{email}}")
	assert.Zero(t, r.smtp.Count())
}

func TestWelcomeEmail(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.uc.SendWelcomeEmail.Execute(context.Background(), "msg-9", usecase.UserRegistered{Email: "bo@example.com"}))
	require.Equal(t, 1, r.smtp.Count())
	assert.Equal(t, "bo@example.com", r.smtp.Sent[0].To)
	assert.Contains(t, r.smtp.Sent[0].HTML, "bo@example.com")
}

func TestUserControlledTextCannotInjectMarkupIntoAnEmail(t *testing.T) {
	r := newRig(t)
	ev := verification()
	ev.Link = `"><script>alert(1)</script>`
	require.NoError(t, r.uc.SendVerificationEmail.Execute(context.Background(), "msg-x", ev))
	assert.NotContains(t, r.smtp.Sent[0].HTML, "<script>")
	assert.Contains(t, r.smtp.Sent[0].HTML, "&lt;script&gt;")
}

// ---- notifications ------------------------------------------------------------------------------------------------

func TestEachEventBecomesTheRightNotification(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	require.NoError(t, r.uc.NotifyProductCreated.Execute(ctx, "m1", usecase.ProductCreated{ProductID: "p1", ShopID: "s1", Name: "Shirt", Price: 150000}))
	require.NoError(t, r.uc.NotifyDiscountCreated.Execute(ctx, "m2", usecase.DiscountCreated{DiscountID: "d1", ShopID: "s1", Code: "SALE10", Name: "Ten off", Type: "percent", Value: 10}))
	require.NoError(t, r.uc.NotifyOrderConfirmed.Execute(ctx, "m3", usecase.OrderConfirmed{OrderID: "o1", UserID: "alice", TotalCheckout: 180000, Items: 2}))
	require.NoError(t, r.uc.NotifyOrderCancelled.Execute(ctx, "m4", usecase.OrderCancelled{OrderID: "o2", UserID: "alice", Reason: "CARD_DECLINED"}))

	all := r.w.All()
	require.Len(t, all, 4)
	byType := map[domain.Type]domain.Notification{}
	for _, n := range all {
		byType[n.Type] = n
		assert.NotContains(t, n.Content, "{{", "every placeholder replaced")
	}
	p := byType[domain.TypeNewProduct]
	assert.Equal(t, domain.Broadcast, p.ReceiverID)
	assert.Equal(t, "s1", p.SenderID)
	assert.Equal(t, "A shop just added a new product: Shirt", p.Content)
	assert.Equal(t, "p1", p.Options["productId"])
	d := byType[domain.TypePromotion]
	assert.Equal(t, domain.Broadcast, d.ReceiverID)
	assert.Equal(t, "A shop just created a new voucher: Ten off (code SALE10)", d.Content)
	c := byType[domain.TypeOrderConfirmed]
	assert.Equal(t, "alice", c.ReceiverID, "orders notify the buyer, not everybody")
	assert.Equal(t, "Your order o1 was confirmed. Total: 180000", c.Content)
	x := byType[domain.TypeOrderCancelled]
	assert.Equal(t, "alice", x.ReceiverID)
	assert.Equal(t, "Your order o2 was cancelled: your card was declined", x.Content)
	assert.Equal(t, "CARD_DECLINED", x.Options["reason"], "the machine-readable reason stays in the options")
}

func TestARedeliveredEventCreatesNoSecondNotification(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ev := usecase.OrderConfirmed{OrderID: "o1", UserID: "alice", TotalCheckout: 1}
	for i := 0; i < 4; i++ {
		require.NoError(t, r.uc.NotifyOrderConfirmed.Execute(ctx, "same-message", ev))
	}
	assert.Len(t, r.w.All(), 1)
	require.NoError(t, r.uc.NotifyOrderConfirmed.Execute(ctx, "another-message", ev))
	assert.Len(t, r.w.All(), 2, "a different message is a different notification")
}

func TestHumanReasons(t *testing.T) {
	r := newRig(t)
	for reason, want := range map[string]string{
		"CARD_DECLINED":        "your card was declined",
		"PROVIDER_UNAVAILABLE": "the payment provider could not be reached",
		"INSUFFICIENT_STOCK":   "an item is no longer available",
		"UNKNOWN_PRODUCT":      "an item is no longer available",
		"STEP_TIMEOUT":         "the order took too long to process",
		"DISCOUNT_USED_UP":     "the discount code could not be used",
		"":                     "the order could not be completed",
		"SOMETHING_NEW":        "the order could not be completed",
	} {
		require.NoError(t, r.uc.NotifyOrderCancelled.Execute(context.Background(), "m-"+reason, usecase.OrderCancelled{OrderID: "o", UserID: "u", Reason: reason}))
		all := r.w.All()
		assert.Contains(t, all[len(all)-1].Content, want, "reason %q", reason)
	}
}

func TestInvalidEventsFailPermanently(t *testing.T) {
	r := newRig(t)
	err := r.uc.NotifyOrderConfirmed.Execute(context.Background(), "m1", usecase.OrderConfirmed{OrderID: "o1"}) // no buyer
	require.Error(t, err)
	assert.True(t, errs.IsPermanent(err))
	err = r.uc.NotifyProductCreated.Execute(context.Background(), "", usecase.ProductCreated{Name: "x"}) // no message id
	assert.Error(t, err)
	assert.Empty(t, r.w.All())
	err = r.uc.SendEmail.Execute(context.Background(), usecase.EmailRequest{To: "a@b.co", Subject: "s", Template: domain.TemplateWelcome, Params: map[string]string{"email": "a@b.co"}})
	assert.True(t, errs.IsPermanent(err), "an email without its message id would share one record with every other")
	assert.Zero(t, r.smtp.Count())
}

// Acceptance #4
func TestListShowsTheCallersNotificationsAndTheBroadcastsNewestFirst(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	steps := []func(){
		func() {
			_ = r.uc.NotifyProductCreated.Execute(ctx, "m1", usecase.ProductCreated{ShopID: "s1", Name: "A"})
		},
		func() {
			_ = r.uc.NotifyOrderConfirmed.Execute(ctx, "m2", usecase.OrderConfirmed{OrderID: "o1", UserID: "alice"})
		},
		func() {
			_ = r.uc.NotifyOrderConfirmed.Execute(ctx, "m3", usecase.OrderConfirmed{OrderID: "o2", UserID: "bob"})
		},
		func() {
			_ = r.uc.NotifyOrderCancelled.Execute(ctx, "m4", usecase.OrderCancelled{OrderID: "o3", UserID: "alice", Reason: "STEP_TIMEOUT"})
		},
		func() {
			_ = r.uc.NotifyDiscountCreated.Execute(ctx, "m5", usecase.DiscountCreated{ShopID: "s1", Code: "C", Name: "N"})
		},
	}
	for _, step := range steps {
		step()
		r.clk.Advance(time.Second)
	}

	res, err := r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{})
	require.NoError(t, err)
	assert.EqualValues(t, 4, res.Total, "alice's two and the two broadcasts, not bob's")
	types := []domain.Type{}
	for _, n := range res.Items {
		types = append(types, n.Type)
	}
	assert.Equal(t, []domain.Type{domain.TypePromotion, domain.TypeOrderCancelled, domain.TypeOrderConfirmed, domain.TypeNewProduct}, types, "newest first")

	only, err := r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{Type: "order-001"})
	require.NoError(t, err)
	require.Len(t, only.Items, 1, "the type filter is case-insensitive")
	all, err := r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{Type: "ALL"})
	require.NoError(t, err)
	assert.EqualValues(t, 4, all.Total)
	page, err := r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{Page: 2, Limit: 3})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	assert.Equal(t, 2, page.Page)

	yes, no := true, false
	read, err := r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{IsRead: &yes})
	require.NoError(t, err)
	assert.Zero(t, read.Total)
	unread, err := r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{IsRead: &no})
	require.NoError(t, err)
	assert.EqualValues(t, 4, unread.Total)

	_, err = r.uc.ListNotifications.Execute(as("alice"), usecase.ListInput{Type: "BOGUS"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = r.uc.ListNotifications.Execute(context.Background(), usecase.ListInput{})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
}

// ---- templates ----------------------------------------------------------------------------------------------------

func TestCreateTemplateAndUseIt(t *testing.T) {
	r := newRig(t)
	tpl, err := r.uc.CreateTemplate.Execute(as("admin"), "Order shipped", "<p>Order {{orderId}} left for {{city}}. Track: {{tracking}}</p>")
	require.NoError(t, err)
	assert.Equal(t, "Order shipped", tpl.Name)

	_, err = r.uc.CreateTemplate.Execute(as("admin"), "Order shipped", "<p>again</p>")
	assert.Equal(t, domain.CodeTemplateExists, errs.CodeOf(err))
	_, err = r.uc.CreateTemplate.Execute(as("admin"), "x", "<p>too short a name</p>")
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = r.uc.CreateTemplate.Execute(context.Background(), "Another", "<p>x</p>")
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))

	// acceptance #1 end to end: a template with three placeholders, all replaced in the mail
	require.NoError(t, r.uc.SendEmail.Execute(context.Background(), usecase.EmailRequest{MessageID: "m1", To: "an@example.com", Subject: "Shipped", Template: "Order shipped",
		Params: map[string]string{"orderId": "o-7", "city": "HCMC", "tracking": "VN123"}}))
	assert.Equal(t, "<p>Order o-7 left for HCMC. Track: VN123</p>", r.smtp.Sent[0].HTML)
}

func TestEnsureTemplatesCreatesMissingDefaultsAndLeavesEditsAlone(t *testing.T) {
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{Notifications: w.Notifications(), Templates: w.Templates(), Emails: w.Emails(), Sender: &fake.Sender{}, Clock: clock.NewFixed(now), IDs: idgen.ULID{}})
	edited, _ := domain.NewTemplate("x", domain.TemplateWelcome, "<p>edited {{email}}</p>", now)
	require.NoError(t, w.Templates().Insert(context.Background(), edited))

	created, err := uc.EnsureTemplates.Execute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, created, "only the missing default")
	got, _ := w.Templates().FindByName(context.Background(), domain.TemplateWelcome)
	assert.Equal(t, "<p>edited {{email}}</p>", got.HTML)
	created, err = uc.EnsureTemplates.Execute(context.Background())
	require.NoError(t, err)
	assert.Zero(t, created, "idempotent")
}
