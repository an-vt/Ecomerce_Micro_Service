package smtpclient_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smtpclient "github.com/an-vt/ecom/notification-service/internal/client/smtp"
	"github.com/an-vt/ecom/notification-service/internal/domain"
)

// server is a minimal SMTP server: it records what a client sends and can be told to refuse a step.
type server struct {
	ln         net.Listener
	mu         sync.Mutex
	from, rcpt string
	data       string
	rejectRcpt bool
	hang       bool
	greeted    int
	wantAuth   string // "user:pass": the server advertises AUTH PLAIN and requires it
}

// startServer applies the options before any connection is accepted, so the test and the server goroutines never race.
func startServer(t *testing.T, opts ...func(*server)) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &server{ln: ln}
	for _, o := range opts {
		o(s)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *server) addr() string { return s.ln.Addr().String() }

func (s *server) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { _, _ = io.WriteString(c, line+"\r\n") }
	if s.hang {
		time.Sleep(5 * time.Second)
		return
	}
	say("220 test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			s.mu.Lock()
			s.greeted++
			s.mu.Unlock()
			if s.wantAuth != "" {
				say("250-test")
				say("250 AUTH PLAIN")
			} else {
				say("250 test")
			}
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			if string(raw) == "\x00"+strings.Replace(s.wantAuth, ":", "\x00", 1) {
				say("235 authenticated")
			} else {
				say("535 bad credentials")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			s.mu.Lock()
			s.from = strings.Trim(strings.TrimSpace(line[len("MAIL FROM:"):]), "<>")
			s.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if s.rejectRcpt {
				say("550 no such user")
				continue
			}
			s.mu.Lock()
			s.rcpt = strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
			s.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func ctx() context.Context { return context.Background() }

func sender(t *testing.T, addr string) *smtpclient.Sender {
	t.Helper()
	s, err := smtpclient.New(smtpclient.Config{Addr: addr, From: "Ecom Shop <no-reply@ecom.local>", Timeout: 2 * time.Second})
	require.NoError(t, err)
	return s
}

func TestSendDeliversAWellFormedMessage(t *testing.T) {
	srv := startServer(t)
	body := `<p>Link: <a href="http://x/?a=1&amp;b=2">go</a> — café ✓</p>` + "\n" + strings.Repeat("long ", 80)
	require.NoError(t, sender(t, srv.addr()).Send(ctx(), domain.Mail{To: "an@example.com", Subject: "Xác nhận email", HTML: body}))

	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Equal(t, "no-reply@ecom.local", srv.from, "the envelope sender is the bare address")
	assert.Equal(t, "an@example.com", srv.rcpt)

	msg, err := mail.ReadMessage(strings.NewReader(srv.data))
	require.NoError(t, err)
	assert.Equal(t, `"Ecom Shop" <no-reply@ecom.local>`, msg.Header.Get("From"))
	assert.Equal(t, "an@example.com", msg.Header.Get("To"))
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, "Xác nhận email", subject, "non-ASCII subjects survive")
	assert.Equal(t, `text/html; charset="UTF-8"`, msg.Header.Get("Content-Type"))
	assert.NotEmpty(t, msg.Header.Get("Message-ID"))
	assert.NotEmpty(t, msg.Header.Get("Date"))
	got, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	require.NoError(t, err)
	// SMTP line endings are CRLF; in HTML they carry no meaning
	assert.Equal(t, body, strings.TrimRight(strings.ReplaceAll(string(got), "\r\n", "\n"), "\n"), "the body, long lines and UTF-8 included, comes out as it went in")
}

func TestSendReportsAServerThatRefusesTheRecipient(t *testing.T) {
	srv := startServer(t, func(s *server) { s.rejectRcpt = true })
	err := sender(t, srv.addr()).Send(ctx(), domain.Mail{To: "ghost@example.com", Subject: "s", HTML: "<p>x</p>"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RCPT TO")
	assert.Contains(t, err.Error(), "550")
}

func TestSendReportsAServerThatIsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // nothing listens there any more
	err = sender(t, addr).Send(ctx(), domain.Mail{To: "an@example.com", Subject: "s", HTML: "<p>x</p>"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect")
}

func TestSendGivesUpOnAServerThatNeverAnswers(t *testing.T) {
	srv := startServer(t, func(s *server) { s.hang = true })
	s, err := smtpclient.New(smtpclient.Config{Addr: srv.addr(), From: "no-reply@ecom.local", Timeout: 300 * time.Millisecond})
	require.NoError(t, err)
	start := time.Now()
	err = s.Send(ctx(), domain.Mail{To: "an@example.com", Subject: "s", HTML: "<p>x</p>"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "bounded by the timeout, not by the server")
}

func TestNewValidatesItsConfiguration(t *testing.T) {
	_, err := smtpclient.New(smtpclient.Config{Addr: "localhost:1025", From: "not an address"})
	assert.ErrorContains(t, err, "From")
	_, err = smtpclient.New(smtpclient.Config{Addr: "no-port", From: "a@b.co"})
	assert.ErrorContains(t, err, "address")
	s, err := smtpclient.New(smtpclient.Config{Addr: "localhost:1025", From: "a@b.co"})
	require.NoError(t, err)
	assert.NotNil(t, s)
}

func TestSendAuthenticatesWhenCredentialsAreConfigured(t *testing.T) {
	srv := startServer(t, func(s *server) { s.wantAuth = "mailer:s3cret" })
	good, err := smtpclient.New(smtpclient.Config{Addr: srv.addr(), From: "no-reply@ecom.local", Username: "mailer", Password: "s3cret", Timeout: 2 * time.Second})
	require.NoError(t, err)
	require.NoError(t, good.Send(ctx(), domain.Mail{To: "an@example.com", Subject: "s", HTML: "<p>x</p>"}))

	bad, err := smtpclient.New(smtpclient.Config{Addr: srv.addr(), From: "no-reply@ecom.local", Username: "mailer", Password: "wrong", Timeout: 2 * time.Second})
	require.NoError(t, err)
	err = bad.Send(ctx(), domain.Mail{To: "an@example.com", Subject: "s", HTML: "<p>x</p>"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth")
	assert.Contains(t, err.Error(), "535")
}
