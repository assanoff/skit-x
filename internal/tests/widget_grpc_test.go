package tests

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/assanoff/skit/logger"

	widgetv1 "github.com/assanoff/skit-x/gen/widget/v1"
	"github.com/assanoff/skit-x/internal/app/server"
)

func TestWidgetGRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires docker")
	}

	ctx := context.Background()
	cfg := startPostgres(ctx, t)

	log := logger.New(io.Discard, logger.Config{Service: "test", Level: logger.LevelError})

	gs, err := server.GRPCServer(ctx, cfg, log)
	if err != nil {
		t.Fatalf("build grpc server: %v", err)
	}

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { _ = gs.Stop(ctx) })

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := widgetv1.NewWidgetServiceClient(conn)

	// Create.
	created, err := client.CreateWidget(ctx, widgetv1.CreateWidgetRequest_builder{Name: "grpc-gadget", Description: "via grpc"}.Build())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.GetWidget().GetId()
	if id == "" || created.GetWidget().GetName() != "grpc-gadget" {
		t.Fatalf("unexpected created widget: %v", created.GetWidget())
	}

	// Get.
	got, err := client.GetWidget(ctx, widgetv1.GetWidgetRequest_builder{Id: id}.Build())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetWidget().GetDescription() != "via grpc" {
		t.Fatalf("unexpected description: %v", got.GetWidget())
	}

	// List.
	list, err := client.ListWidgets(ctx, &widgetv1.ListWidgetsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.GetWidgets()) != 1 {
		t.Fatalf("expected 1 widget, got %d", len(list.GetWidgets()))
	}

	// Update (partial: description only).
	newDesc := "updated via grpc"
	upd, err := client.UpdateWidget(ctx, widgetv1.UpdateWidgetRequest_builder{Id: id, Description: &newDesc}.Build())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.GetWidget().GetDescription() != newDesc || upd.GetWidget().GetName() != "grpc-gadget" {
		t.Fatalf("update mismatch: %v", upd.GetWidget())
	}

	// Validation error -> InvalidArgument.
	_, err = client.CreateWidget(ctx, widgetv1.CreateWidgetRequest_builder{Description: "no name"}.Build())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}

	// Unknown id -> NotFound.
	_, err = client.GetWidget(ctx, widgetv1.GetWidgetRequest_builder{Id: uuid.NewString()}.Build())
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v (%v)", status.Code(err), err)
	}

	// Bad id -> InvalidArgument.
	_, err = client.GetWidget(ctx, widgetv1.GetWidgetRequest_builder{Id: "not-a-uuid"}.Build())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for bad id, got %v", status.Code(err))
	}

	// Delete -> then NotFound.
	if _, err := client.DeleteWidget(ctx, widgetv1.DeleteWidgetRequest_builder{Id: id}.Build()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = client.GetWidget(ctx, widgetv1.GetWidgetRequest_builder{Id: id}.Build())
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound after delete, got %v", status.Code(err))
	}
}
