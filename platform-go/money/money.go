// Package money holds amounts as integers in the smallest currency unit (docs/04-data-model.md). No floats.
package money

import (
	"fmt"
	"math"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Amount is a number of minor units (VND has none, so 150000 means 150 000 ₫).
type Amount int64

// CodeOverflow is returned when an operation would overflow int64.
const CodeOverflow = "MONEY_OVERFLOW"

// Add returns a+b, or an error on overflow.
func (a Amount) Add(b Amount) (Amount, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, errs.InvalidArgument(CodeOverflow, "amount overflow")
	}
	return a + b, nil
}

// Sub returns a-b, or an error on overflow.
func (a Amount) Sub(b Amount) (Amount, error) {
	if b == math.MinInt64 {
		return 0, errs.InvalidArgument(CodeOverflow, "amount overflow")
	}
	return a.Add(-b)
}

// Mul returns a*qty, or an error on overflow. qty must not be negative.
func (a Amount) Mul(qty int64) (Amount, error) {
	if qty < 0 {
		return 0, errs.Validation("quantity must not be negative")
	}
	if a == 0 || qty == 0 {
		return 0, nil
	}
	p := int64(a) * qty
	if p/qty != int64(a) || (a == -1 && qty == math.MinInt64) {
		return 0, errs.InvalidArgument(CodeOverflow, "amount overflow")
	}
	return Amount(p), nil
}

// Percent returns a*pct/100 rounded half-up (away from zero for negatives). pct must be 0..100.
func (a Amount) Percent(pct int) (Amount, error) {
	if pct < 0 || pct > 100 {
		return 0, errs.Validation("percent must be between 0 and 100")
	}
	scaled, err := a.Mul(int64(pct))
	if err != nil {
		// a is negative here or the product overflowed; handle negatives symmetrically
		if a < 0 {
			n, nerr := (-a).Percent(pct)
			return -n, nerr
		}
		return 0, err
	}
	if scaled >= 0 {
		return (scaled + 50) / 100, nil
	}
	return -((-scaled + 50) / 100), nil
}

// Min returns the smaller of a and b.
func Min(a, b Amount) Amount {
	if a < b {
		return a
	}
	return b
}

// IsNegative reports whether a < 0.
func (a Amount) IsNegative() bool { return a < 0 }

// String formats the amount as a plain integer.
func (a Amount) String() string { return fmt.Sprintf("%d", int64(a)) }
