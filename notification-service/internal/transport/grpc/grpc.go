// Package grpc exposes the notification use cases as the NotificationService gRPC service (proto/notification/v1).
package grpc

import (
	"context"
	"time"

	"github.com/an-vt/ecom/notification-service/internal/usecase"
	notificationv1 "github.com/an-vt/ecom/proto/gen/go/notification/v1"
)

// Server implements notificationv1.NotificationServiceServer.
type Server struct {
	notificationv1.UnimplementedNotificationServiceServer
	uc *usecase.Set
}

// NewServer creates the handler.
func NewServer(uc *usecase.Set) *Server { return &Server{uc: uc} }

func (s *Server) ListNotifications(ctx context.Context, in *notificationv1.ListNotificationsRequest) (*notificationv1.ListNotificationsResponse, error) {
	res, err := s.uc.ListNotifications.Execute(ctx, usecase.ListInput{Type: in.GetType(), IsRead: in.IsRead, Page: int(in.GetPage()), Limit: int(in.GetLimit())})
	if err != nil {
		return nil, err
	}
	out := &notificationv1.ListNotificationsResponse{Total: res.Total}
	for _, n := range res.Items {
		out.Notifications = append(out.Notifications, &notificationv1.Notification{Id: n.ID, Type: string(n.Type), SenderId: n.SenderID, ReceiverId: n.ReceiverID,
			Content: n.Content, OptionsJson: n.OptionsJSON(), IsRead: n.IsRead, CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return out, nil
}

func (s *Server) CreateTemplate(ctx context.Context, in *notificationv1.CreateTemplateRequest) (*notificationv1.Template, error) {
	t, err := s.uc.CreateTemplate.Execute(ctx, in.GetName(), in.GetHtml())
	if err != nil {
		return nil, err
	}
	return &notificationv1.Template{Id: t.ID, Name: t.Name, Html: t.HTML, Status: t.Status, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339Nano)}, nil
}
