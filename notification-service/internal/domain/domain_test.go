package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// Acceptance #1
func TestRenderReplacesEveryPlaceholder(t *testing.T) {
	got, err := domain.Render("Hello {{name}}, your order {{orderId}} totals {{total}}. Bye {{name}}.",
		map[string]string{"name": "An", "orderId": "o-1", "total": "180000"}, false)
	require.NoError(t, err)
	assert.Equal(t, "Hello An, your order o-1 totals 180000. Bye An.", got, "all three, and a repeated one every time")
}

// The monolith applied each key to the ORIGINAL template, so only the last key survived.
func TestRenderDoesNotRepeatTheMonolithsBug(t *testing.T) {
	got, err := domain.Render("{{a}} {{b}} {{c}}", map[string]string{"a": "1", "b": "2", "c": "3"}, false)
	require.NoError(t, err)
	assert.Equal(t, "1 2 3", got)
	for i := 0; i < 20; i++ { // map iteration order is random: the result must not depend on it
		again, _ := domain.Render("{{a}} {{b}} {{c}}", map[string]string{"a": "1", "b": "2", "c": "3"}, false)
		require.Equal(t, "1 2 3", again)
	}
}

func TestRenderIsASinglePass(t *testing.T) {
	got, err := domain.Render("{{name}} / {{secret}}", map[string]string{"name": "{{secret}}", "secret": "s3cr3t"}, false)
	require.NoError(t, err)
	assert.Equal(t, "{{secret}} / s3cr3t", got, "a value is inserted as text and never expanded again")
}

func TestRenderRefusesAMissingValueInsteadOfSendingABrokenEmail(t *testing.T) {
	_, err := domain.Render("{{link}} {{expiresAt}} {{zeta}}", map[string]string{"link": "x"}, false)
	require.Error(t, err)
	assert.Equal(t, domain.CodeMissingParam, errs.CodeOf(err))
	assert.True(t, errs.IsPermanent(err), "retrying cannot supply a value")
	assert.Contains(t, err.Error(), "expiresAt, zeta", "the names, sorted")
	assert.NotContains(t, err.Error(), "link")

	got, err := domain.Render("no placeholders", nil, true)
	require.NoError(t, err)
	assert.Equal(t, "no placeholders", got)
	got, err = domain.Render("{{a}}", map[string]string{"a": "1", "unused": "2"}, false)
	require.NoError(t, err, "extra parameters are fine")
	assert.Equal(t, "1", got)
}

func TestRenderEscapesValuesInHTMLButNotTheTemplate(t *testing.T) {
	got, err := domain.Render(`<a href="{{link}}">{{name}}</a>`, map[string]string{"link": "https://x/?a=1&b=2", "name": `<script>alert("x")</script>`}, true)
	require.NoError(t, err)
	assert.Equal(t, `<a href="https://x/?a=1&amp;b=2">&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;</a>`, got)
	plain, err := domain.Render("{{name}}", map[string]string{"name": "<b>&"}, false)
	require.NoError(t, err)
	assert.Equal(t, "<b>&", plain, "plain text is not escaped")
}

func TestRenderToleratesSpacesInsideTheBraces(t *testing.T) {
	got, err := domain.Render("{{ token }}|{{token}}|{{  token}}", map[string]string{"token": "T"}, false)
	require.NoError(t, err)
	assert.Equal(t, "T|T|T", got)
}

func TestPlaceholders(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c.d"}, domain.Placeholders("{{b}} {{a}} {{b}} {{ c.d }} {x} {{}}"))
	assert.Empty(t, domain.Placeholders("plain"))
}

func TestTheDefaultTemplatesRenderWithTheirParameters(t *testing.T) {
	for _, tpl := range domain.DefaultTemplates() {
		params := map[string]string{}
		for _, p := range domain.Placeholders(tpl.HTML) {
			params[p] = "v-" + p
		}
		out, err := domain.Render(tpl.HTML, params, true)
		require.NoError(t, err, tpl.Name)
		assert.NotContains(t, out, "{{", tpl.Name)
		_, err = domain.NewTemplate("id", tpl.Name, tpl.HTML, now)
		require.NoError(t, err, "a default is a valid template: "+tpl.Name)
	}
	names := []string{}
	for _, tpl := range domain.DefaultTemplates() {
		names = append(names, tpl.Name)
	}
	assert.ElementsMatch(t, []string{domain.TemplateEmailVerification, domain.TemplateWelcome}, names)
	verification := domain.DefaultTemplates()[0]
	assert.Equal(t, []string{"expiresAt", "link"}, domain.Placeholders(verification.HTML))
}

func TestNewTemplate(t *testing.T) {
	tpl, err := domain.NewTemplate("t1", "  Order shipped ", "<p>{{orderId}}</p>", now)
	require.NoError(t, err)
	assert.Equal(t, "Order shipped", tpl.Name)
	assert.Equal(t, "active", tpl.Status)
	assert.Equal(t, now, tpl.CreatedAt)

	for name, args := range map[string][3]string{
		"no id":      {"", "ok", "<p>x</p>"},
		"short name": {"i", "a", "<p>x</p>"},
		"odd name":   {"i", "bad/name", "<p>x</p>"},
		"newline":    {"i", "bad\nname", "<p>x</p>"},
		"empty body": {"i", "ok", "   "},
		"huge body":  {"i", "ok", strings.Repeat("x", domain.MaxTemplateBytes+1)},
	} {
		_, err := domain.NewTemplate(args[0], args[1], args[2], now)
		require.Error(t, err, name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), name)
	}
}

func TestNotifications(t *testing.T) {
	n, err := domain.NewNotification("n1", domain.TypeNewProduct, "shop1", domain.Broadcast, "hello", map[string]any{"productId": "p1"}, "msg-1", now)
	require.NoError(t, err)
	assert.False(t, n.IsRead)
	assert.JSONEq(t, `{"productId":"p1"}`, n.OptionsJSON())
	empty, _ := domain.NewNotification("n2", domain.TypePromotion, "", "u1", "x", nil, "msg-2", now)
	assert.JSONEq(t, `{}`, empty.OptionsJSON(), "never null")

	bad := &domain.Notification{Options: map[string]any{"f": func() {}}}
	assert.Equal(t, "{}", bad.OptionsJSON(), "unserialisable options degrade to an empty object")

	for name, f := range map[string]func() (*domain.Notification, error){
		"no id": func() (*domain.Notification, error) {
			return domain.NewNotification("", domain.TypePromotion, "s", "u", "c", nil, "k", now)
		},
		"no dedupe": func() (*domain.Notification, error) {
			return domain.NewNotification("i", domain.TypePromotion, "s", "u", "c", nil, "", now)
		},
		"unknown type": func() (*domain.Notification, error) {
			return domain.NewNotification("i", "BOGUS", "s", "u", "c", nil, "k", now)
		},
		"no receiver": func() (*domain.Notification, error) {
			return domain.NewNotification("i", domain.TypePromotion, "s", "", "c", nil, "k", now)
		},
		"no content": func() (*domain.Notification, error) {
			return domain.NewNotification("i", domain.TypePromotion, "s", "u", "", nil, "k", now)
		},
	} {
		_, err := f()
		assert.Error(t, err, name)
	}
	for _, typ := range []domain.Type{domain.TypeOrderConfirmed, domain.TypeOrderCancelled, domain.TypePromotion, domain.TypeNewProduct} {
		assert.True(t, typ.Valid())
	}
	assert.False(t, domain.Type("").Valid())
}

func TestEmailRecords(t *testing.T) {
	e, err := domain.NewEmail("e1", "msg-1:welcome", " an@example.com ", "Welcome", "welcome", now)
	require.NoError(t, err)
	assert.Equal(t, "an@example.com", e.To)
	assert.Equal(t, domain.EmailPending, e.Status)

	e.MarkFailed(errors.New("smtp: connection refused"), now.Add(time.Second))
	assert.Equal(t, domain.EmailFailed, e.Status)
	assert.Equal(t, "smtp: connection refused", e.LastError)
	assert.Equal(t, 1, e.Attempts)
	e.MarkSent(now.Add(2 * time.Second))
	assert.Equal(t, domain.EmailSent, e.Status)
	assert.Empty(t, e.LastError, "a success clears the last error")
	assert.Equal(t, 2, e.Attempts)
	assert.Equal(t, now.Add(2*time.Second), e.UpdatedAt)
}

// A header value with a line break is how mail gets injected.
func TestEmailRefusesHeaderInjection(t *testing.T) {
	for _, to := range []string{"", "no-at-sign", "a@b@c", "a@", "@b.c", "a@b.c\r\nBcc: x@y.z", "a@b.c, d@e.f", "a b@c.d", "<a@b.c>", "a@b.c;d@e.f"} {
		_, err := domain.NewEmail("e", "k", to, "s", "t", now)
		require.Error(t, err, to)
		assert.Equal(t, domain.CodeInvalidEmail, errs.CodeOf(err), to)
		assert.True(t, errs.IsPermanent(err))
	}
	_, err := domain.NewEmail("e", "k", "a@b.co", "Hi\r\nBcc: x@y.z", "t", now)
	assert.Equal(t, domain.CodeInvalidEmail, errs.CodeOf(err), "the subject too")
	for _, args := range [][3]string{{"", "k", "t"}, {"e", "", "t"}, {"e", "k", ""}} {
		_, err := domain.NewEmail(args[0], args[1], "a@b.co", "s", args[2], now)
		assert.Error(t, err)
	}
	_, err = domain.NewEmail("e", "k", strings.Repeat("a", 320)+"@b.co", "s", "t", now)
	assert.Error(t, err, "longer than any address can be")
}

func TestErrors(t *testing.T) {
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrTemplateNotFound("x")))
	assert.Contains(t, domain.ErrTemplateNotFound("x").Error(), "x")
	assert.Equal(t, errs.KindAlreadyExists, errs.KindOf(domain.ErrTemplateExists()))
}
