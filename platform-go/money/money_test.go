package money_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
)

func TestAddSub(t *testing.T) {
	got, err := money.Amount(100).Add(50)
	require.NoError(t, err)
	assert.Equal(t, money.Amount(150), got)

	got, err = money.Amount(100).Sub(130)
	require.NoError(t, err)
	assert.Equal(t, money.Amount(-30), got)
	assert.True(t, got.IsNegative())

	_, err = money.Amount(math.MaxInt64).Add(1)
	assert.Equal(t, money.CodeOverflow, errs.CodeOf(err))
	_, err = money.Amount(math.MinInt64).Add(-1)
	assert.Equal(t, money.CodeOverflow, errs.CodeOf(err))
	_, err = money.Amount(0).Sub(math.MinInt64)
	assert.Equal(t, money.CodeOverflow, errs.CodeOf(err))
}

func TestMul(t *testing.T) {
	tests := []struct {
		name    string
		a       money.Amount
		qty     int64
		want    money.Amount
		wantErr string
	}{
		{"simple", 150000, 3, 450000, ""},
		{"zero amount", 0, 5, 0, ""},
		{"zero qty", 99, 0, 0, ""},
		{"negative qty", 10, -1, 0, errs.CodeValidationFailed},
		{"overflow", math.MaxInt64, 2, 0, money.CodeOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.a.Mul(tt.qty)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tt.wantErr, errs.CodeOf(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPercentRoundsHalfUp(t *testing.T) {
	tests := []struct {
		name string
		a    money.Amount
		pct  int
		want money.Amount
	}{
		{"exact", 200000, 10, 20000},
		{"round up .5", 5, 10, 1},   // 0.5 -> 1
		{"round down .4", 4, 10, 0}, // 0.4 -> 0
		{"zero percent", 1000, 0, 0},
		{"full", 1234, 100, 1234},
		{"negative rounds away from zero", -5, 10, -1},
		{"negative exact", -200, 50, -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.a.Percent(tt.pct)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPercentErrors(t *testing.T) {
	_, err := money.Amount(10).Percent(101)
	assert.Equal(t, errs.CodeValidationFailed, errs.CodeOf(err))
	_, err = money.Amount(10).Percent(-1)
	assert.Equal(t, errs.CodeValidationFailed, errs.CodeOf(err))
	_, err = money.Amount(math.MaxInt64).Percent(50)
	assert.Equal(t, money.CodeOverflow, errs.CodeOf(err))
	_, err = money.Amount(math.MinInt64 + 1).Percent(50)
	assert.Error(t, err)
}

func TestMinAndString(t *testing.T) {
	assert.Equal(t, money.Amount(3), money.Min(3, 9))
	assert.Equal(t, money.Amount(3), money.Min(9, 3))
	assert.Equal(t, "42", money.Amount(42).String())
}
