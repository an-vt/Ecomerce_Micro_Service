package rbac_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/an-vt/ecom/platform-go/rbac"
)

var grants = []rbac.Grant{
	{Role: "user", Resource: "profile", Actions: []string{"readOwn", "updateOwn"}, Attributes: "*"},
	{Role: "shop", Resource: "profile", Actions: []string{"readOwn"}},
	{Role: "shop", Resource: "product", Actions: []string{"createOwn", "updateOwn", "deleteOwn"}},
	{Role: "admin", Resource: "profile", Actions: []string{"readAny", "updateAny"}},
	{Role: "admin", Resource: "rbac", Actions: []string{"createAny", "readAny"}},
	{Role: "admin", Resource: "profile", Actions: []string{"deleteAny"}}, // second grant for the same pair merges
}

func TestCheckReturnsTheBroadestPossession(t *testing.T) {
	e := rbac.New(grants)
	assert.Equal(t, rbac.Own, e.Check([]string{"user"}, "read", "profile"))
	assert.Equal(t, rbac.Any, e.Check([]string{"admin"}, "read", "profile"))
	assert.Equal(t, rbac.Any, e.Check([]string{"user", "admin"}, "read", "profile"), "roles combine: any beats own, in any order")
	assert.Equal(t, rbac.Any, e.Check([]string{"admin", "user"}, "read", "profile"))
	assert.Equal(t, rbac.None, e.Check([]string{"user"}, "delete", "profile"))
	assert.Equal(t, rbac.None, e.Check([]string{"user"}, "read", "rbac"))
	assert.Equal(t, rbac.None, e.Check([]string{"ghost"}, "read", "profile"))
	assert.Equal(t, rbac.None, e.Check(nil, "read", "profile"))
	assert.Equal(t, rbac.Any, e.Check([]string{"admin"}, "delete", "profile"), "grants of the same role/resource merge")
}

func TestCanUsesAccessControlVocabulary(t *testing.T) {
	e := rbac.New(grants)
	tests := []struct {
		roles    []string
		action   string
		resource string
		want     bool
	}{
		{[]string{"user"}, "readOwn", "profile", true},
		{[]string{"user"}, "readAny", "profile", false},
		{[]string{"admin"}, "readAny", "profile", true},
		{[]string{"admin"}, "readOwn", "profile", true}, // any implies own
		{[]string{"shop"}, "createOwn", "product", true},
		{[]string{"shop"}, "createAny", "product", false},
		{[]string{"user"}, "createOwn", "product", false},
		{[]string{"admin"}, "createAny", "rbac", true},
		{[]string{"shop"}, "readAny", "rbac", false},
		{[]string{"admin"}, "read", "profile", true}, // bare verb = strictest reading (any)
		{[]string{"user"}, "read", "profile", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, e.Can(tt.roles, tt.action, tt.resource), "%v %s %s", tt.roles, tt.action, tt.resource)
	}
}

func TestEmptyEvaluatorDeniesEverything(t *testing.T) {
	e := rbac.New(nil)
	assert.False(t, e.Can([]string{"admin"}, "readAny", "profile"))
	assert.Equal(t, rbac.None, e.Check([]string{"admin"}, "read", "profile"))
}
