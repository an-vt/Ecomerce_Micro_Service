//go:build integration

package mongorepo_test

import (
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

var repotestBase = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func errsCode(err error) string { return errs.CodeOf(err) }
