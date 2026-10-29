// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fakes and
// against MongoDB, so a fake can never promise something the real repository does not do.
package repotest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Repos are the repositories under test.
type Repos struct {
	Notifications domain.NotificationRepository
	Templates     domain.TemplateRepository
	Emails        domain.EmailRepository
}

// New returns a fresh, empty set of repositories for one test.
type New func(t *testing.T) Repos

var ctx = context.Background()

// Base is the time the suite's fixtures start at.
var Base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// Run executes the whole suite.
func Run(t *testing.T, newRepos New) {
	t.Run("Notifications", func(t *testing.T) { notifications(t, newRepos(t)) })
	t.Run("Listing", func(t *testing.T) { listing(t, newRepos(t)) })
	t.Run("Templates", func(t *testing.T) { templates(t, newRepos(t)) })
	t.Run("Emails", func(t *testing.T) { emails(t, newRepos(t)) })
}

func note(t *testing.T, n int, typ domain.Type, receiver, key string) *domain.Notification {
	t.Helper()
	x, err := domain.NewNotification(fmt.Sprintf("n%03d", n), typ, "sender", receiver, fmt.Sprintf("content %d", n),
		map[string]any{"n": float64(n), "nested": map[string]any{"a": "b"}}, key, Base.Add(time.Duration(n)*time.Second))
	require.NoError(t, err)
	return x
}

func notifications(t *testing.T, r Repos) {
	n := note(t, 1, domain.TypeOrderConfirmed, "alice", "msg-1")
	inserted, err := r.Notifications.Insert(ctx, n)
	require.NoError(t, err)
	assert.True(t, inserted)

	got, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice"}, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, n, got[0], "round trip keeps every field, including nested options")

	// the same message again, even with a different notification id, creates nothing
	again := note(t, 2, domain.TypeOrderConfirmed, "alice", "msg-1")
	inserted, err = r.Notifications.Insert(ctx, again)
	require.NoError(t, err, "a duplicate is not an error")
	assert.False(t, inserted)
	_, total, _ = r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice"}, 0, 10)
	assert.EqualValues(t, 1, total)
}

// Acceptance #4: the receiver's notifications plus the broadcasts, newest first.
func listing(t *testing.T, r Repos) {
	for i, spec := range []struct {
		typ      domain.Type
		receiver string
	}{
		{domain.TypeNewProduct, domain.Broadcast}, // 1
		{domain.TypeOrderConfirmed, "alice"},      // 2
		{domain.TypeOrderConfirmed, "bob"},        // 3: someone else's
		{domain.TypePromotion, domain.Broadcast},  // 4
		{domain.TypeOrderCancelled, "alice"},      // 5
	} {
		_, err := r.Notifications.Insert(ctx, note(t, i+1, spec.typ, spec.receiver, fmt.Sprintf("k%d", i+1)))
		require.NoError(t, err)
	}

	ids := func(ns []*domain.Notification) []string {
		out := make([]string, len(ns))
		for i, n := range ns {
			out[i] = n.ID
		}
		return out
	}
	all, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice"}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 4, total, "alice's two and the two broadcasts, not bob's")
	assert.Equal(t, []string{"n005", "n004", "n002", "n001"}, ids(all), "newest first")

	page, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice"}, 1, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 4, total, "the total ignores the page")
	assert.Equal(t, []string{"n004", "n002"}, ids(page))
	past, _, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice"}, 50, 10)
	require.NoError(t, err)
	assert.Empty(t, past)

	byType, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice", Type: domain.TypeOrderConfirmed}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, []string{"n002"}, ids(byType))

	unread, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice", IsRead: boolp(false)}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 4, total)
	assert.Len(t, unread, 4)
	read, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "alice", IsRead: boolp(true)}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, total)
	assert.Empty(t, read)

	stranger, total, err := r.Notifications.List(ctx, domain.ListFilter{ReceiverID: "nobody"}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "a stranger still sees the broadcasts")
	assert.Equal(t, []string{"n004", "n001"}, ids(stranger))
}

func boolp(b bool) *bool { return &b }

func templates(t *testing.T, r Repos) {
	tpl, err := domain.NewTemplate("t1", "Order shipped", "<p>{{orderId}}</p>", Base)
	require.NoError(t, err)
	require.NoError(t, r.Templates.Insert(ctx, tpl))

	got, err := r.Templates.FindByName(ctx, "Order shipped")
	require.NoError(t, err)
	assert.Equal(t, tpl, got, "round trip")
	_, err = r.Templates.FindByName(ctx, "nope")
	assert.Equal(t, domain.CodeTemplateNotFound, errs.CodeOf(err))

	dup, _ := domain.NewTemplate("t2", "Order shipped", "<p>other</p>", Base)
	assert.Equal(t, domain.CodeTemplateExists, errs.CodeOf(r.Templates.Insert(ctx, dup)))
	inserted, err := r.Templates.InsertIfAbsent(ctx, dup)
	require.NoError(t, err)
	assert.False(t, inserted, "an existing template is left alone")
	got, _ = r.Templates.FindByName(ctx, "Order shipped")
	assert.Equal(t, "<p>{{orderId}}</p>", got.HTML, "and keeps its body")

	fresh, _ := domain.NewTemplate("t3", "Brand new", "<p>x</p>", Base)
	inserted, err = r.Templates.InsertIfAbsent(ctx, fresh)
	require.NoError(t, err)
	assert.True(t, inserted)
}

func emails(t *testing.T, r Repos) {
	e, err := domain.NewEmail("e1", "msg-1:welcome", "an@example.com", "Welcome", "welcome", Base)
	require.NoError(t, err)
	stored, err := r.Emails.Begin(ctx, e)
	require.NoError(t, err)
	assert.Equal(t, e, stored)

	// a second Begin for the same message returns the stored record, not the new one
	other, _ := domain.NewEmail("e2", "msg-1:welcome", "other@example.com", "x", "welcome", Base.Add(time.Hour))
	again, err := r.Emails.Begin(ctx, other)
	require.NoError(t, err)
	assert.Equal(t, "e1", again.ID)
	assert.Equal(t, "an@example.com", again.To)

	stored.MarkFailed(fmt.Errorf("smtp down"), Base.Add(time.Second))
	require.NoError(t, r.Emails.Save(ctx, stored))
	reread, err := r.Emails.Begin(ctx, other)
	require.NoError(t, err)
	assert.Equal(t, domain.EmailFailed, reread.Status)
	assert.Equal(t, "smtp down", reread.LastError)
	assert.Equal(t, 1, reread.Attempts)

	reread.MarkSent(Base.Add(2 * time.Second))
	require.NoError(t, r.Emails.Save(ctx, reread))
	final, _ := r.Emails.Begin(ctx, other)
	assert.Equal(t, domain.EmailSent, final.Status)
	assert.Empty(t, final.LastError)
	assert.Equal(t, 2, final.Attempts)

	ghost, _ := domain.NewEmail("e9", "msg-9:welcome", "a@b.co", "s", "welcome", Base)
	assert.Error(t, r.Emails.Save(ctx, ghost), "saving an email that was never begun")
}
