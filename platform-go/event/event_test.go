package event_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
)

type productCreated struct {
	ProductID string `json:"productId"`
	Price     int64  `json:"price"`
}

func (p productCreated) EventType() string   { return "catalog.product.created" }
func (p productCreated) AggregateID() string { return p.ProductID }

func TestEnvelopeRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.FixedZone("x", 7*3600))
	env, err := event.NewEnvelope("m1", "ReserveStock", "o1", "o1", at, map[string]any{"orderId": "o1"})
	require.NoError(t, err)
	assert.Equal(t, time.UTC, env.OccurredAt.Location())

	raw, err := env.Marshal()
	require.NoError(t, err)
	got, err := event.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "ReserveStock", got.Type)
	assert.True(t, got.OccurredAt.Equal(at))

	var payload struct {
		OrderID string `json:"orderId"`
	}
	require.NoError(t, got.Decode(&payload))
	assert.Equal(t, "o1", payload.OrderID)
}

func TestFromEvent(t *testing.T) {
	env, err := event.FromEvent("m2", time.Unix(0, 0), productCreated{ProductID: "p1", Price: 10})
	require.NoError(t, err)
	assert.Equal(t, "catalog.product.created", env.Type)
	assert.Equal(t, "p1", env.AggregateID)
	assert.Equal(t, "p1", env.CorrelationID)
	var got productCreated
	require.NoError(t, env.Decode(&got))
	assert.Equal(t, int64(10), got.Price)
}

func TestNewEnvelopeRejectsUnserialisablePayload(t *testing.T) {
	_, err := event.NewEnvelope("m", "T", "a", "", time.Now(), map[string]any{"f": math.Inf(1)})
	require.Error(t, err)
	assert.Equal(t, event.CodeInvalidEnvelope, errs.CodeOf(err))
	assert.True(t, errs.IsPermanent(err))
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, in string }{
		{"not json", `{`},
		{"missing id", `{"type":"T","payload":{}}`},
		{"missing type", `{"messageId":"m","payload":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := event.Parse([]byte(tt.in))
			require.Error(t, err)
			assert.Equal(t, event.CodeInvalidEnvelope, errs.CodeOf(err))
			assert.True(t, errs.IsPermanent(err))
		})
	}
}

func TestDecodeMismatch(t *testing.T) {
	env, err := event.NewEnvelope("m", "T", "a", "", time.Now(), "just a string")
	require.NoError(t, err)
	var v struct{ X int }
	err = env.Decode(&v)
	require.Error(t, err)
	assert.Equal(t, event.CodeInvalidEnvelope, errs.CodeOf(err))
}
