package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func input() domain.NewInput {
	return domain.NewInput{ID: "d1", ShopID: "s1", Code: "sale-10", Name: "Ten", Type: domain.TypePercent, Value: 10,
		StartDate: now.Add(-time.Hour), EndDate: now.Add(24 * time.Hour), MaxUses: 5, MaxUsesPerUser: 1}
}

func mk(t *testing.T, mut func(*domain.NewInput)) *domain.Discount {
	t.Helper()
	in := input()
	if mut != nil {
		mut(&in)
	}
	d, err := domain.New(in, now)
	require.NoError(t, err)
	return d
}

func TestNewNormalizesAndValidates(t *testing.T) {
	d := mk(t, func(in *domain.NewInput) { in.Code = "  sale-10 " })
	s := d.Snapshot()
	assert.Equal(t, "SALE-10", s.Code, "codes are upper-cased")
	assert.True(t, s.IsActive)
	assert.Equal(t, domain.ScopeAll, s.AppliesTo)
	assert.Empty(t, s.ProductIDs)

	cases := map[string]func(*domain.NewInput){
		"short code":          func(in *domain.NewInput) { in.Code = "ab" },
		"bad characters":      func(in *domain.NewInput) { in.Code = "SALE 10" },
		"no name":             func(in *domain.NewInput) { in.Name = " " },
		"unknown type":        func(in *domain.NewInput) { in.Type = "bogus" },
		"zero value":          func(in *domain.NewInput) { in.Value = 0 },
		"percent over 100":    func(in *domain.NewInput) { in.Value = 101 },
		"no uses":             func(in *domain.NewInput) { in.MaxUses = 0 },
		"negative user limit": func(in *domain.NewInput) { in.MaxUsesPerUser = -1 },
		"negative min order":  func(in *domain.NewInput) { in.MinOrderValue = -1 },
		"no dates":            func(in *domain.NewInput) { in.StartDate = time.Time{} },
		"end before start":    func(in *domain.NewInput) { in.EndDate = in.StartDate },
		"already ended":       func(in *domain.NewInput) { in.StartDate, in.EndDate = now.Add(-48*time.Hour), now.Add(-time.Hour) },
		"specific, no list":   func(in *domain.NewInput) { in.AppliesTo = domain.ScopeSpecific },
		"unknown scope":       func(in *domain.NewInput) { in.AppliesTo = "some" },
		"no shop":             func(in *domain.NewInput) { in.ShopID = "" },
	}
	for name, mut := range cases {
		in := input()
		mut(&in)
		_, err := domain.New(in, now)
		require.Error(t, err, name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), name)
	}
}

func TestSpecificScopeDeduplicatesProducts(t *testing.T) {
	d := mk(t, func(in *domain.NewInput) {
		in.AppliesTo, in.ProductIDs = domain.ScopeSpecific, []string{"a", " a ", "b", ""}
	})
	assert.Equal(t, []string{"a", "b"}, d.Snapshot().ProductIDs)
	big := make([]string, domain.MaxProducts+1)
	for i := range big {
		big[i] = string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('0'+i/676))
	}
	in := input()
	in.AppliesTo, in.ProductIDs = domain.ScopeSpecific, big
	_, err := domain.New(in, now)
	assert.Error(t, err)
}

// Acceptance #1: percent / fixed / specific products produce exactly these amounts.
func TestCalculateAmounts(t *testing.T) {
	lines := []domain.Line{{ProductID: "a", Price: 100000, Quantity: 2}, {ProductID: "b", Price: 50000, Quantity: 1}} // subtotal 250000
	tests := []struct {
		name                                string
		mut                                 func(*domain.NewInput)
		subtotal, eligible, discount, total int64
	}{
		{"10 percent of everything", nil, 250000, 250000, 25000, 225000},
		{"percent rounds half up", func(in *domain.NewInput) { in.Value = 7 }, 250000, 250000, 17500, 232500},
		{"fixed amount", func(in *domain.NewInput) { in.Type, in.Value = domain.TypeFixedAmount, 30000 }, 250000, 250000, 30000, 220000},
		{"fixed amount is capped at the covered value", func(in *domain.NewInput) {
			in.Type, in.Value, in.AppliesTo, in.ProductIDs = domain.TypeFixedAmount, 80000, domain.ScopeSpecific, []string{"b"}
		}, 250000, 50000, 50000, 200000},
		{"percent of the covered products only", func(in *domain.NewInput) {
			in.Value, in.AppliesTo, in.ProductIDs = 50, domain.ScopeSpecific, []string{"a"}
		}, 250000, 200000, 100000, 150000},
		{"100 percent", func(in *domain.NewInput) { in.Value = 100 }, 250000, 250000, 250000, 0},
		{"minimum met exactly", func(in *domain.NewInput) { in.MinOrderValue = 250000 }, 250000, 250000, 25000, 225000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mk(t, tc.mut).Calculate(lines)
			require.NoError(t, err)
			assert.EqualValues(t, tc.subtotal, got.Subtotal)
			assert.EqualValues(t, tc.eligible, got.Eligible)
			assert.EqualValues(t, tc.discount, got.Discount)
			assert.EqualValues(t, tc.total, got.Total())
		})
	}
}

func TestCalculateRefusals(t *testing.T) {
	lines := []domain.Line{{ProductID: "a", Price: 1000, Quantity: 1}}
	_, err := mk(t, func(in *domain.NewInput) { in.MinOrderValue = 5000 }).Calculate(lines)
	assert.Equal(t, domain.CodeMinOrderNotReached, errs.CodeOf(err))
	_, err = mk(t, func(in *domain.NewInput) {
		in.AppliesTo, in.ProductIDs = domain.ScopeSpecific, []string{"zzz"}
	}).Calculate(lines)
	assert.Equal(t, domain.CodeNotApplicable, errs.CodeOf(err), "none of the products is covered")
	_, err = mk(t, nil).Calculate(nil)
	assert.Equal(t, domain.CodeNotApplicable, errs.CodeOf(err), "an empty order")

	for _, bad := range [][]domain.Line{
		{{ProductID: "a", Price: 1, Quantity: 0}},
		{{ProductID: "a", Price: -1, Quantity: 1}},
		{{ProductID: "a", Price: 1 << 62, Quantity: 4}},
	} {
		_, err = mk(t, nil).Calculate(bad)
		assert.Error(t, err)
	}
	_, err = mk(t, nil).Calculate([]domain.Line{{ProductID: "a", Price: 1 << 62, Quantity: 1}, {ProductID: "b", Price: 1 << 62, Quantity: 1}, {ProductID: "c", Price: 1 << 62, Quantity: 1}})
	assert.Error(t, err, "overflow is an error, not a wrap-around")
}

func TestCanBeUsedBy(t *testing.T) {
	d := mk(t, func(in *domain.NewInput) { in.MaxUses = 2 })
	assert.NoError(t, d.CanBeUsedBy(0, now))
	assert.Equal(t, domain.CodeUserLimitReached, errs.CodeOf(d.CanBeUsedBy(1, now)), "per-user limit of 1")
	assert.Equal(t, domain.CodeNotStarted, errs.CodeOf(d.CanBeUsedBy(0, now.Add(-2*time.Hour))))
	assert.Equal(t, domain.CodeExpired, errs.CodeOf(d.CanBeUsedBy(0, now.Add(24*time.Hour))), "the end instant itself is already expired")
	assert.NoError(t, d.CanBeUsedBy(0, now.Add(24*time.Hour-time.Second)))

	require.NoError(t, d.Use(now))
	require.NoError(t, d.Use(now))
	assert.Equal(t, domain.CodeUsedUp, errs.CodeOf(d.CanBeUsedBy(0, now)))
	assert.Equal(t, domain.CodeUsedUp, errs.CodeOf(d.Use(now)), "Use re-checks under the lock")
	d.Unuse(now)
	assert.NoError(t, d.CanBeUsedBy(0, now))

	off := false
	require.NoError(t, d.Update(domain.Patch{IsActive: &off}, now))
	assert.Equal(t, domain.CodeInactive, errs.CodeOf(d.CanBeUsedBy(0, now)))

	unlimited := mk(t, func(in *domain.NewInput) { in.MaxUsesPerUser = 0 })
	assert.NoError(t, unlimited.CanBeUsedBy(1000, now), "0 means no per-user limit")
}

func TestUnuseNeverGoesBelowZero(t *testing.T) {
	d := mk(t, nil)
	d.Unuse(now)
	assert.EqualValues(t, 0, d.Snapshot().UsedCount)
}

func TestUpdate(t *testing.T) {
	d := mk(t, nil)
	require.NoError(t, d.Use(now))
	later := now.Add(48 * time.Hour)
	name, desc, uses, per, min := " New name ", "d", int64(10), int64(3), int64(1000)
	all := domain.ScopeSpecific
	require.NoError(t, d.Update(domain.Patch{Name: &name, Description: &desc, EndDate: &later, MaxUses: &uses, MaxUsesPerUser: &per,
		MinOrderValue: &min, AppliesTo: &all, ProductIDs: []string{"p1"}, ReplaceProducts: true}, now.Add(time.Minute)))
	s := d.Snapshot()
	assert.Equal(t, "New name", s.Name)
	assert.Equal(t, "d", s.Description)
	assert.Equal(t, later, s.EndDate)
	assert.EqualValues(t, 10, s.MaxUses)
	assert.EqualValues(t, 3, s.MaxUsesPerUser)
	assert.EqualValues(t, 1000, s.MinOrderValue)
	assert.Equal(t, []string{"p1"}, s.ProductIDs)
	assert.Equal(t, now.Add(time.Minute), s.UpdatedAt)
	assert.EqualValues(t, 1, s.UsedCount, "the counter is not editable")

	require.NoError(t, d.Use(now)) // two uses made
	before := d.Snapshot()
	belowUsed := int64(1)
	empty, early, tooFew, neg := "", s.StartDate, int64(0), int64(-1)
	specific := domain.ScopeSpecific
	for name, p := range map[string]domain.Patch{
		"empty name":        {Name: &empty},
		"end before start":  {EndDate: &early},
		"max_uses below 1":  {MaxUses: &tooFew},
		"below used":        {MaxUses: &belowUsed},
		"negative per user": {MaxUsesPerUser: &neg},
		"negative min":      {MinOrderValue: &neg},
		"specific, no list": {AppliesTo: &specific, ProductIDs: nil, ReplaceProducts: true},
	} {
		assert.Error(t, d.Update(p, now), name)
		assert.Equal(t, before, d.Snapshot(), name+": a refused update changes nothing")
	}

	// "all" drops the list
	scopeAll := domain.ScopeAll
	require.NoError(t, d.Update(domain.Patch{AppliesTo: &scopeAll}, now))
	assert.Empty(t, d.Snapshot().ProductIDs)
}

func TestSnapshotIsACopy(t *testing.T) {
	d := mk(t, func(in *domain.NewInput) { in.AppliesTo, in.ProductIDs = domain.ScopeSpecific, []string{"a"} })
	s := d.Snapshot()
	s.ProductIDs[0] = "hacked"
	assert.Equal(t, []string{"a"}, d.Snapshot().ProductIDs)
	assert.Equal(t, "d1", d.ID())
	assert.Equal(t, "s1", d.ShopID())
	assert.Equal(t, "SALE-10", d.Code())
	assert.Equal(t, s.Code, domain.Restore(s).Code())
}

func TestUsage(t *testing.T) {
	u, err := domain.NewUsage("u1", "d1", "user", "o1", 500, now)
	require.NoError(t, err)
	assert.Equal(t, domain.UsageApplied, u.Status())
	assert.Equal(t, "d1", u.DiscountID())
	assert.EqualValues(t, 500, u.Amount())
	assert.True(t, u.Release(now.Add(time.Second)))
	assert.False(t, u.Release(now.Add(2*time.Second)), "releasing twice changes nothing")
	assert.Equal(t, now.Add(time.Second), u.Snapshot().UpdatedAt)
	assert.Equal(t, u.Snapshot(), domain.RestoreUsage(u.Snapshot()).Snapshot())
	for _, args := range [][4]string{{"", "d", "u", "o"}, {"i", "", "u", "o"}, {"i", "d", "", "o"}, {"i", "d", "u", ""}} {
		_, err := domain.NewUsage(args[0], args[1], args[2], args[3], 1, now)
		assert.Error(t, err)
	}
	_, err = domain.NewUsage("i", "d", "u", "o", -1, now)
	assert.Error(t, err)
}

func TestErrorsHaveStableCodes(t *testing.T) {
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrNotFound()))
	assert.Equal(t, errs.KindAlreadyExists, errs.KindOf(domain.ErrCodeTaken()))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(domain.ErrNotOwner()))
}
