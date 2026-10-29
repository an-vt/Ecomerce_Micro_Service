// Package fake provides in-memory implementations of the notification ports for unit tests. The repository contract
// suite (internal/repotest) runs against these fakes and against MongoDB.
package fake

import (
	"context"
	"sort"
	"sync"

	"github.com/an-vt/ecom/notification-service/internal/domain"
)

// World is all the state of the fakes.
type World struct {
	mu            sync.Mutex
	notifications []domain.Notification
	templates     map[string]domain.Template // by name
	emails        map[string]domain.Email    // by dedupe key
}

// NewWorld creates an empty world.
func NewWorld() *World {
	return &World{templates: map[string]domain.Template{}, emails: map[string]domain.Email{}}
}

// ---- notifications ------------------------------------------------------------------------------------------------

type notificationRepo struct{ w *World }

// Notifications returns the notification repository.
func (w *World) Notifications() domain.NotificationRepository { return notificationRepo{w} }

func (r notificationRepo) Insert(_ context.Context, n *domain.Notification) (bool, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, e := range r.w.notifications {
		if e.DedupeKey == n.DedupeKey {
			return false, nil
		}
	}
	r.w.notifications = append(r.w.notifications, *n)
	return true, nil
}

func (r notificationRepo) List(_ context.Context, f domain.ListFilter, offset, limit int) ([]*domain.Notification, int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var match []domain.Notification
	for _, n := range r.w.notifications {
		if n.ReceiverID != f.ReceiverID && n.ReceiverID != domain.Broadcast {
			continue
		}
		if f.Type != "" && n.Type != f.Type {
			continue
		}
		if f.IsRead != nil && n.IsRead != *f.IsRead {
			continue
		}
		match = append(match, n)
	}
	sort.Slice(match, func(i, j int) bool { // newest first, id as the tie-break
		if !match[i].CreatedAt.Equal(match[j].CreatedAt) {
			return match[i].CreatedAt.After(match[j].CreatedAt)
		}
		return match[i].ID > match[j].ID
	})
	total := int64(len(match))
	offset = min(offset, len(match))
	end := min(offset+limit, len(match))
	out := make([]*domain.Notification, 0, end-offset)
	for i := offset; i < end; i++ {
		n := match[i]
		out = append(out, &n)
	}
	return out, total, nil
}

// All returns every stored notification, oldest first.
func (w *World) All() []domain.Notification {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]domain.Notification(nil), w.notifications...)
}

// ---- templates ----------------------------------------------------------------------------------------------------

type templateRepo struct{ w *World }

// Templates returns the template repository.
func (w *World) Templates() domain.TemplateRepository { return templateRepo{w} }

func (r templateRepo) Insert(ctx context.Context, t *domain.Template) error {
	ok, err := r.InsertIfAbsent(ctx, t)
	if err == nil && !ok {
		return domain.ErrTemplateExists()
	}
	return err
}

func (r templateRepo) InsertIfAbsent(_ context.Context, t *domain.Template) (bool, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.templates[t.Name]; ok {
		return false, nil
	}
	r.w.templates[t.Name] = *t
	return true, nil
}

func (r templateRepo) FindByName(_ context.Context, name string) (*domain.Template, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if t, ok := r.w.templates[name]; ok {
		return &t, nil
	}
	return nil, domain.ErrTemplateNotFound(name)
}

// ---- emails -------------------------------------------------------------------------------------------------------

type emailRepo struct{ w *World }

// Emails returns the email repository.
func (w *World) Emails() domain.EmailRepository { return emailRepo{w} }

func (r emailRepo) Begin(_ context.Context, e *domain.Email) (*domain.Email, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if existing, ok := r.w.emails[e.DedupeKey]; ok {
		return &existing, nil
	}
	r.w.emails[e.DedupeKey] = *e
	c := *e
	return &c, nil
}

func (r emailRepo) Save(_ context.Context, e *domain.Email) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.emails[e.DedupeKey]; !ok {
		return domain.ErrTemplateNotFound("email " + e.DedupeKey)
	}
	r.w.emails[e.DedupeKey] = *e
	return nil
}

// Email returns the stored record of an email by its dedupe key.
func (w *World) Email(dedupeKey string) (domain.Email, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.emails[dedupeKey]
	return e, ok
}

// Sender is a scriptable SMTP sender that records what it was asked to send.
type Sender struct {
	mu   sync.Mutex
	Sent []domain.Mail
	Err  error
}

// Send implements domain.Sender.
func (s *Sender) Send(_ context.Context, m domain.Mail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.Sent = append(s.Sent, m)
	return nil
}

// Count is how many mails were sent.
func (s *Sender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Sent)
}
