// Package e2ekit holds the helpers of the end-to-end suites (docs/10-testing.md §3): a small JSON/HTTP client for the
// gateway, access to the Kafka topics to read what services published, and skip-or-fail handling when the stack is
// not running.
package e2ekit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Env returns the environment variable k or def.
func Env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// RequireReachable skips the test when addr (host:port) is not reachable — or fails it when E2E_REQUIRED=1, so CI
// cannot pass by silently skipping.
func RequireReachable(t testing.TB, what, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		if os.Getenv("E2E_REQUIRED") == "1" {
			t.Fatalf("%s is not reachable at %s: %v", what, addr, err)
		}
		t.Skipf("%s is not running at %s", what, addr)
	}
	_ = c.Close()
}

// UniqueEmail returns an address nobody used before.
func UniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@e2e.test", prefix, time.Now().UnixNano())
}

// Client talks to the gateway.
type Client struct {
	Base   string
	APIKey string
	HTTP   *http.Client
}

// NewClient reads GATEWAY_URL (default http://127.0.0.1:8088) and SEED_API_KEY and checks the gateway is up.
func NewClient(t testing.TB) *Client {
	t.Helper()
	base := Env("GATEWAY_URL", "http://127.0.0.1:8088")
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("GATEWAY_URL: %v", err)
	}
	RequireReachable(t, "api-gateway", u.Host)
	return &Client{Base: strings.TrimRight(base, "/"), APIKey: Env("SEED_API_KEY", "dev-api-key-change-me"), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Response of one call.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Cookies []*http.Cookie
}

// Req describes one call.
type Req struct {
	Method, Path string
	Body         any
	Token        string            // Authorization: Bearer
	NoAPIKey     bool              // omit x-api-key
	Headers      map[string]string // extra headers
	Cookies      []*http.Cookie
}

// Do sends the request and returns the response (never fails on a non-2xx status).
func (c *Client) Do(t testing.TB, r Req) *Response {
	t.Helper()
	var body io.Reader
	if r.Body != nil {
		b, err := json.Marshal(r.Body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(r.Method, c.Base+r.Path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !r.NoAPIKey {
		req.Header.Set("x-api-key", c.APIKey)
	}
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	for _, ck := range r.Cookies {
		req.AddCookie(ck)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.Method, r.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b, Cookies: resp.Cookies()}
}

// JSON decodes the body into a generic map.
func (r *Response) JSON() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	return m
}

// Get walks a dotted path ("metadata.tokens.accessToken") through the JSON body.
func (r *Response) Get(path string) any {
	var cur any = r.JSON()
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

// String is Get for string values ("" when absent).
func (r *Response) String(path string) string { s, _ := r.Get(path).(string); return s }

// Code is the error code of an error envelope.
func (r *Response) Code() string { return r.String("error") }

// Cookie returns the named Set-Cookie of the response.
func (r *Response) Cookie(name string) *http.Cookie {
	for _, c := range r.Cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// WaitForEvent reads topic from the beginning until match accepts an envelope (or fails after 30 s).
func WaitForEvent(t testing.TB, topic string, match func(event.Envelope) bool) event.Envelope {
	t.Helper()
	brokers := strings.Split(Env("KAFKA_BROKERS", "127.0.0.1:9094"), ",")
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("kafka client: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var found *event.Envelope
		cl.PollFetches(ctx).EachRecord(func(r *kgo.Record) {
			if env, err := event.Parse(r.Value); err == nil && found == nil && match(env) {
				found = &env
			}
		})
		if found != nil {
			return *found
		}
	}
	t.Fatalf("no matching event on %s within 30s", topic)
	return event.Envelope{}
}

// SendSagaCommand publishes a saga command to its topic, keyed by the order id, like order-service does.
func SendSagaCommand(t testing.TB, topic string, m saga.Message) {
	t.Helper()
	brokers := strings.Split(Env("KAFKA_BROKERS", "127.0.0.1:9094"), ",")
	p, err := kafkax.NewProducer(brokers)
	if err != nil {
		t.Fatalf("kafka producer: %v", err)
	}
	defer p.Close()
	env, err := saga.Envelope(fmt.Sprintf("e2e-%d", time.Now().UnixNano()), time.Now(), m)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.Publish(ctx, kafkax.Message{Topic: topic, Key: m.Order(), Value: raw}); err != nil {
		t.Fatalf("publish %s: %v", m.MessageType(), err)
	}
}

// WaitForReply waits for a saga reply of the given type for an order on order.saga.replies.
func WaitForReply(t testing.TB, orderID, typ string, into any) {
	t.Helper()
	env := WaitForEvent(t, saga.TopicReplies, func(e event.Envelope) bool { return e.AggregateID == orderID && e.Type == typ })
	if into != nil {
		if err := env.Decode(into); err != nil {
			t.Fatalf("decode %s: %v", typ, err)
		}
	}
}
