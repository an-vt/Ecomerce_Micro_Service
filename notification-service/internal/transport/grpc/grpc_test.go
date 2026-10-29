package grpc_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/notification-service/internal/fake"
	transport "github.com/an-vt/ecom/notification-service/internal/transport/grpc"
	"github.com/an-vt/ecom/notification-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/metrics"
	notificationv1 "github.com/an-vt/ecom/proto/gen/go/notification/v1"
)

func start(t *testing.T) (notificationv1.NotificationServiceClient, *usecase.Set, *clock.Fixed) {
	t.Helper()
	w := fake.NewWorld()
	clk := clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	uc := usecase.NewSet(&usecase.Deps{Notifications: w.Notifications(), Templates: w.Templates(), Emails: w.Emails(), Sender: &fake.Sender{}, Clock: clk, IDs: idgen.ULID{}})
	lis := bufconn.Listen(1 << 20)
	srv := grpcx.NewServer("notification-service", metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	notificationv1.RegisterNotificationServiceServer(srv, transport.NewServer(uc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return grpcx.FromStatus(inv(ctx, m, req, reply, cc, o...))
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return notificationv1.NewNotificationServiceClient(conn), uc, clk
}

func as(id, typ string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), grpcx.MDAccountID, id, grpcx.MDAccountType, typ, grpcx.MDRoles, "x")
}

func TestNotificationsOverGRPC(t *testing.T) {
	c, uc, clk := start(t)
	ctx := context.Background()
	require.NoError(t, uc.NotifyProductCreated.Execute(ctx, "m1", usecase.ProductCreated{ProductID: "p1", ShopID: "s1", Name: "Shirt", Price: 100}))
	clk.Advance(time.Second)
	require.NoError(t, uc.NotifyOrderConfirmed.Execute(ctx, "m2", usecase.OrderConfirmed{OrderID: "o1", UserID: "alice", TotalCheckout: 5000}))
	clk.Advance(time.Second)
	require.NoError(t, uc.NotifyOrderConfirmed.Execute(ctx, "m3", usecase.OrderConfirmed{OrderID: "o2", UserID: "bob", TotalCheckout: 1}))

	res, err := c.ListNotifications(as("alice", "USER"), &notificationv1.ListNotificationsRequest{Page: 1, Limit: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 2, res.GetTotal(), "her own and the broadcast")
	require.Len(t, res.GetNotifications(), 2)
	first := res.GetNotifications()[0]
	assert.Equal(t, "ORDER-001", first.GetType())
	assert.Equal(t, "alice", first.GetReceiverId())
	assert.JSONEq(t, `{"orderId":"o1","total":5000,"items":0}`, first.GetOptionsJson())
	assert.NotEmpty(t, first.GetCreatedAt())

	isRead := false
	res, err = c.ListNotifications(as("alice", "USER"), &notificationv1.ListNotificationsRequest{Type: "SHOP-001", IsRead: &isRead})
	require.NoError(t, err)
	require.Len(t, res.GetNotifications(), 1)
	assert.Equal(t, "*", res.GetNotifications()[0].GetReceiverId())

	_, err = c.ListNotifications(as("alice", "USER"), &notificationv1.ListNotificationsRequest{Type: "BOGUS"})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = c.ListNotifications(context.Background(), &notificationv1.ListNotificationsRequest{})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
}

func TestCreateTemplateOverGRPC(t *testing.T) {
	c, _, _ := start(t)
	tpl, err := c.CreateTemplate(as("admin", "USER"), &notificationv1.CreateTemplateRequest{Name: "Order shipped", Html: "<p>{{orderId}}</p>"})
	require.NoError(t, err)
	assert.Equal(t, "Order shipped", tpl.GetName())
	assert.Equal(t, "active", tpl.GetStatus())
	assert.NotEmpty(t, tpl.GetId())

	_, err = c.CreateTemplate(as("admin", "USER"), &notificationv1.CreateTemplateRequest{Name: "Order shipped", Html: "<p>again</p>"})
	assert.Equal(t, domain.CodeTemplateExists, errs.CodeOf(err))
	_, err = c.CreateTemplate(as("admin", "USER"), &notificationv1.CreateTemplateRequest{Name: "ok name", Html: ""})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
}
