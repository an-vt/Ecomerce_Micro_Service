// Package fake provides in-memory implementations of the auth ports for unit tests. The World is transactional:
// WithinTx rolls every store back when the function fails, so atomicity (account + session + outbox, or nothing)
// can be asserted without a database. The same repository contract suite (internal/repotest) runs against these
// fakes and against PostgreSQL, which keeps the two from drifting apart.
package fake

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/principal"
)

// World is all the state of the fakes.
type World struct {
	mu        sync.Mutex
	accounts  map[string]domain.AccountSnapshot
	sessions  map[string]domain.SessionSnapshot
	otps      map[string]domain.OtpSnapshot
	apiKeys   map[string]domain.ApiKey // by key hash
	roles     map[string]domain.Role
	resources map[string]domain.Resource

	Outbox []outbox.Message
	Denied map[string]time.Duration // revoked session ids -> ttl

	// Failure injection for tests.
	FailOutbox   error
	FailDenylist error
}

// NewWorld creates an empty world.
func NewWorld() *World {
	return &World{
		accounts: map[string]domain.AccountSnapshot{}, sessions: map[string]domain.SessionSnapshot{},
		otps: map[string]domain.OtpSnapshot{}, apiKeys: map[string]domain.ApiKey{},
		roles: map[string]domain.Role{}, resources: map[string]domain.Resource{}, Denied: map[string]time.Duration{},
	}
}

type state struct {
	accounts  map[string]domain.AccountSnapshot
	sessions  map[string]domain.SessionSnapshot
	otps      map[string]domain.OtpSnapshot
	apiKeys   map[string]domain.ApiKey
	roles     map[string]domain.Role
	resources map[string]domain.Resource
	outbox    []outbox.Message
}

func clone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (w *World) save() state {
	w.mu.Lock()
	defer w.mu.Unlock()
	return state{clone(w.accounts), clone(w.sessions), clone(w.otps), clone(w.apiKeys), clone(w.roles), clone(w.resources), append([]outbox.Message(nil), w.Outbox...)}
}

func (w *World) restore(s state) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.accounts, w.sessions, w.otps, w.apiKeys, w.roles, w.resources, w.Outbox = s.accounts, s.sessions, s.otps, s.apiKeys, s.roles, s.resources, s.outbox
}

// WithinTx runs fn and restores every store if it fails.
func (w *World) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	before := w.save()
	if err := fn(ctx); err != nil {
		w.restore(before)
		return err
	}
	return nil
}

// Add implements the outbox port.
func (w *World) Add(_ context.Context, msgs ...outbox.Message) error {
	if w.FailOutbox != nil {
		return w.FailOutbox
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Outbox = append(w.Outbox, msgs...)
	return nil
}

// Revoke implements the denylist port.
func (w *World) Revoke(_ context.Context, sessionID string, ttl time.Duration) error {
	if w.FailDenylist != nil {
		return w.FailDenylist
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Denied[sessionID] = ttl
	return nil
}

// OutboxTypes lists the types of the recorded messages, in order.
func (w *World) OutboxTypes() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.Outbox))
	for i, m := range w.Outbox {
		out[i] = m.Type
	}
	return out
}

// AccountCount returns the number of stored accounts.
func (w *World) AccountCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.accounts) }

// SessionCount returns the number of stored sessions.
func (w *World) SessionCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.sessions) }

// ---- accounts ---------------------------------------------------------------------------------------------------

type accountRepo struct{ w *World }

// Accounts returns the account repository.
func (w *World) Accounts() domain.AccountRepository { return accountRepo{w} }

func (r accountRepo) Create(_ context.Context, a *domain.Account) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, e := range r.w.accounts {
		if e.Type == a.Type() && e.Email == a.Email() {
			return domain.ErrEmailTaken()
		}
	}
	r.w.accounts[a.ID()] = a.Snapshot()
	return nil
}

func (r accountRepo) FindByID(_ context.Context, id string) (*domain.Account, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.accounts[id]; ok {
		return domain.RestoreAccount(s), nil
	}
	return nil, domain.ErrAccountNotFound()
}

func (r accountRepo) FindByEmail(_ context.Context, typ principal.Type, email domain.Email) (*domain.Account, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, s := range r.w.accounts {
		if s.Type == typ && s.Email == email {
			return domain.RestoreAccount(s), nil
		}
	}
	return nil, domain.ErrAccountNotFound()
}

func (r accountRepo) Update(_ context.Context, a *domain.Account) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.accounts[a.ID()]; !ok {
		return domain.ErrAccountNotFound()
	}
	r.w.accounts[a.ID()] = a.Snapshot()
	return nil
}

// ---- sessions ---------------------------------------------------------------------------------------------------

type sessionRepo struct{ w *World }

// Sessions returns the session repository.
func (w *World) Sessions() domain.SessionRepository { return sessionRepo{w} }

func (r sessionRepo) Create(_ context.Context, s *domain.Session) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	r.w.sessions[s.ID()] = s.Snapshot()
	return nil
}

func (r sessionRepo) find(pred func(domain.SessionSnapshot) bool) (*domain.Session, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, s := range r.w.sessions {
		if pred(s) {
			return domain.RestoreSession(s), nil
		}
	}
	return nil, domain.ErrTokenInvalid()
}

func (r sessionRepo) FindByID(_ context.Context, id string) (*domain.Session, error) {
	s, err := r.find(func(s domain.SessionSnapshot) bool { return s.ID == id })
	if err != nil {
		return nil, errs.NotFound("SESSION_NOT_FOUND", "session not found")
	}
	return s, nil
}

func (r sessionRepo) FindByRefreshHash(_ context.Context, hash string) (*domain.Session, error) {
	return r.find(func(s domain.SessionSnapshot) bool { return s.RefreshHash == hash })
}

func (r sessionRepo) FindByUsedHash(_ context.Context, hash string) (*domain.Session, error) {
	return r.find(func(s domain.SessionSnapshot) bool {
		for _, u := range s.UsedHashes {
			if u == hash {
				return true
			}
		}
		return false
	})
}

func (r sessionRepo) Rotate(_ context.Context, s *domain.Session, oldHash string) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	cur, ok := r.w.sessions[s.ID()]
	if !ok || cur.RefreshHash != oldHash || cur.RevokedAt != nil {
		return domain.ErrTokenInvalid()
	}
	r.w.sessions[s.ID()] = s.Snapshot()
	return nil
}

func (r sessionRepo) Revoke(_ context.Context, s *domain.Session) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.sessions[s.ID()]; !ok {
		return domain.ErrTokenInvalid()
	}
	r.w.sessions[s.ID()] = s.Snapshot()
	return nil
}

// ---- otps -------------------------------------------------------------------------------------------------------

type otpRepo struct{ w *World }

// Otps returns the one-time token repository.
func (w *World) Otps() domain.OtpRepository { return otpRepo{w} }

func (r otpRepo) Create(_ context.Context, o *domain.Otp) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	r.w.otps[o.ID()] = o.Snapshot()
	return nil
}

func (r otpRepo) FindByTokenHash(_ context.Context, hash string) (*domain.Otp, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, s := range r.w.otps {
		if s.TokenHash == hash {
			return domain.RestoreOtp(s), nil
		}
	}
	return nil, domain.ErrTokenInvalid()
}

func (r otpRepo) MarkUsed(_ context.Context, o *domain.Otp) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	cur, ok := r.w.otps[o.ID()]
	if !ok || cur.UsedAt != nil {
		return domain.ErrTokenInvalid()
	}
	r.w.otps[o.ID()] = o.Snapshot()
	return nil
}

// ---- api keys ---------------------------------------------------------------------------------------------------

type apiKeyRepo struct{ w *World }

// ApiKeys returns the API key repository.
func (w *World) ApiKeys() domain.ApiKeyRepository { return apiKeyRepo{w} }

func (r apiKeyRepo) FindByHash(_ context.Context, hash string) (*domain.ApiKey, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if k, ok := r.w.apiKeys[hash]; ok {
		return &k, nil
	}
	return nil, nil
}

func (r apiKeyRepo) Save(_ context.Context, k *domain.ApiKey) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	r.w.apiKeys[k.KeyHash] = *k
	return nil
}

// ---- rbac -------------------------------------------------------------------------------------------------------

type rbacRepo struct{ w *World }

// Rbac returns the RBAC repository.
func (w *World) Rbac() domain.RbacRepository { return rbacRepo{w} }

func (r rbacRepo) CreateRole(_ context.Context, role *domain.Role) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.roles[role.Name]; ok {
		return errs.AlreadyExists(domain.CodeRoleExists, "role "+role.Name+" already exists")
	}
	r.w.roles[role.Name] = *role
	return nil
}

func (r rbacRepo) UpsertRole(_ context.Context, role *domain.Role) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if cur, ok := r.w.roles[role.Name]; ok {
		role.ID = cur.ID
	}
	r.w.roles[role.Name] = *role
	return nil
}

func (r rbacRepo) ListRoles(_ context.Context) ([]domain.Role, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	out := make([]domain.Role, 0, len(r.w.roles))
	for _, role := range r.w.roles {
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r rbacRepo) CreateResource(_ context.Context, res *domain.Resource) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.resources[res.Name]; ok {
		return errs.AlreadyExists(domain.CodeResourceExists, "resource "+res.Name+" already exists")
	}
	r.w.resources[res.Name] = *res
	return nil
}

func (r rbacRepo) UpsertResource(_ context.Context, res *domain.Resource) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if cur, ok := r.w.resources[res.Name]; ok {
		res.ID = cur.ID
	}
	r.w.resources[res.Name] = *res
	return nil
}

func (r rbacRepo) ListResources(_ context.Context) ([]domain.Resource, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	out := make([]domain.Resource, 0, len(r.w.resources))
	for _, res := range r.w.resources {
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r rbacRepo) MissingResources(_ context.Context, names []string) ([]string, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var missing []string
	for _, n := range names {
		if _, ok := r.w.resources[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

func (r rbacRepo) ActiveGrants(_ context.Context) ([]domain.RoleGrant, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var out []domain.RoleGrant
	for _, role := range r.w.roles {
		if role.Status != domain.RoleActive {
			continue
		}
		for _, g := range role.Grants {
			out = append(out, domain.RoleGrant{Role: role.Name, Resource: g.Resource, Actions: g.Actions, Attributes: g.Attributes})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Resource < out[j].Resource
	})
	return out, nil
}

// ---- small adapters ---------------------------------------------------------------------------------------------

// Hasher is a reversible stand-in for bcrypt; it counts calls so tests can assert timing-equalising comparisons.
type Hasher struct {
	mu       sync.Mutex
	Compares int
	FailHash error
}

// Hash implements usecase.PasswordHasher.
func (h *Hasher) Hash(plain string) (string, error) {
	if h.FailHash != nil {
		return "", h.FailHash
	}
	return "hashed:" + plain, nil
}

// Compare implements usecase.PasswordHasher.
func (h *Hasher) Compare(hash, plain string) error {
	h.mu.Lock()
	h.Compares++
	h.mu.Unlock()
	if hash != "hashed:"+plain {
		return fmt.Errorf("mismatch")
	}
	return nil
}

// Secrets is a deterministic SecretGenerator: secret-1, secret-2, …
type Secrets struct {
	mu   sync.Mutex
	n    int
	Fail error
}

// New implements usecase.SecretGenerator.
func (s *Secrets) New() (string, string, error) {
	if s.Fail != nil {
		return "", "", s.Fail
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	secret := fmt.Sprintf("secret-%d", s.n)
	return secret, domain.HashToken(secret), nil
}

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

// TestKey returns one RSA key shared by all tests of a binary (generating a key per test is slow).
func TestKey() *rsa.PrivateKey {
	keyOnce.Do(func() { testKey, _ = rsa.GenerateKey(rand.Reader, 2048) })
	return testKey
}

// NewSigner returns a real RS256 signer over the shared test key.
func NewSigner(ttl time.Duration, clk interface{ Now() time.Time }) *jwtx.Signer {
	return jwtx.NewSigner(TestKey(), "ecom-auth", "ecom", ttl, clkAdapter{clk})
}

type clkAdapter struct{ c interface{ Now() time.Time } }

func (a clkAdapter) Now() time.Time { return a.c.Now() }
