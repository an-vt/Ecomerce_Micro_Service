package grpcx

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/principal"
)

// Metadata keys (docs/07 §5).
const (
	MDRequestID   = "x-request-id"
	MDAccountID   = "x-account-id"
	MDAccountType = "x-account-type"
	MDRoles       = "x-account-roles"
	MDSessionID   = "x-session-id"
)

// outgoing copies the request id and principal from ctx into outgoing metadata.
func outgoing(ctx context.Context) context.Context {
	var kv []string
	if id := logger.RequestID(ctx); id != "" {
		kv = append(kv, MDRequestID, id)
	}
	if p, ok := principal.From(ctx); ok {
		kv = append(kv, MDAccountID, p.ID, MDAccountType, string(p.Type), MDRoles, strings.Join(p.Roles, ","))
		if p.SessionID != "" {
			kv = append(kv, MDSessionID, p.SessionID)
		}
	}
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// incoming restores the request id and principal from incoming metadata into ctx.
func incoming(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	first := func(k string) string {
		if v := md.Get(k); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if id := first(MDRequestID); id != "" {
		ctx = logger.WithRequestID(ctx, id)
	}
	if id := first(MDAccountID); id != "" {
		p := principal.Principal{ID: id, Type: principal.Type(first(MDAccountType)), SessionID: first(MDSessionID)}
		if roles := first(MDRoles); roles != "" {
			p.Roles = strings.Split(roles, ",")
		}
		ctx = principal.With(ctx, p)
	}
	return ctx
}
