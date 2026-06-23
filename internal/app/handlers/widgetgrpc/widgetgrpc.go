// Package widgetgrpc is the gRPC transport for the widget module. Like the REST
// handler it is a thin adapter over widget.Core: it maps protobuf messages to
// the domain and returns *errs.Error values, which the server's interceptor
// converts into gRPC statuses.
package widgetgrpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/assanoff/servicekit/errs"

	"github.com/assanoff/service-kit-x/core/widget"
	widgetv1 "github.com/assanoff/service-kit-x/gen/widget/v1"
)

// Handler implements widgetv1.WidgetServiceServer.
type Handler struct {
	widgetv1.UnimplementedWidgetServiceServer
	core *widget.Core
}

// New builds a Handler.
func New(core *widget.Core) *Handler { return &Handler{core: core} }

// CreateWidget implements the gRPC WidgetService.
func (h *Handler) CreateWidget(ctx context.Context, req *widgetv1.CreateWidgetRequest) (*widgetv1.CreateWidgetResponse, error) {
	nw := widget.NewWidget{Name: req.GetName(), Description: req.GetDescription()}
	if err := errs.Check(struct {
		Name string `validate:"required,max=100"`
	}{Name: nw.Name}); err != nil {
		return nil, err
	}

	w, err := h.core.Create(ctx, nw)
	if err != nil {
		return nil, err
	}
	return widgetv1.CreateWidgetResponse_builder{Widget: toProto(w)}.Build(), nil
}

// GetWidget implements the gRPC WidgetService.
func (h *Handler) GetWidget(ctx context.Context, req *widgetv1.GetWidgetRequest) (*widgetv1.GetWidgetResponse, error) {
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	w, err := h.core.QueryByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return widgetv1.GetWidgetResponse_builder{Widget: toProto(w)}.Build(), nil
}

// ListWidgets implements the gRPC WidgetService.
func (h *Handler) ListWidgets(ctx context.Context, _ *widgetv1.ListWidgetsRequest) (*widgetv1.ListWidgetsResponse, error) {
	ws, err := h.core.Query(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*widgetv1.Widget, len(ws))
	for i, w := range ws {
		out[i] = toProto(w)
	}
	return widgetv1.ListWidgetsResponse_builder{Widgets: out}.Build(), nil
}

// UpdateWidget implements the gRPC WidgetService.
func (h *Handler) UpdateWidget(ctx context.Context, req *widgetv1.UpdateWidgetRequest) (*widgetv1.UpdateWidgetResponse, error) {
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	var uw widget.UpdateWidget
	if req.HasName() {
		v := req.GetName()
		uw.Name = &v
	}
	if req.HasDescription() {
		v := req.GetDescription()
		uw.Description = &v
	}

	w, err := h.core.Update(ctx, id, uw)
	if err != nil {
		return nil, err
	}
	return widgetv1.UpdateWidgetResponse_builder{Widget: toProto(w)}.Build(), nil
}

// DeleteWidget implements the gRPC WidgetService.
func (h *Handler) DeleteWidget(ctx context.Context, req *widgetv1.DeleteWidgetRequest) (*widgetv1.DeleteWidgetResponse, error) {
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := h.core.Delete(ctx, id); err != nil {
		return nil, err
	}
	return widgetv1.DeleteWidgetResponse_builder{}.Build(), nil
}

func parseID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errs.Newf(errs.InvalidArgument, "invalid id %q", s)
	}
	return id, nil
}

func toProto(w widget.Widget) *widgetv1.Widget {
	return widgetv1.Widget_builder{
		Id:          w.ID.String(),
		Name:        w.Name,
		Description: w.Description,
		CreatedAt:   timestamppb.New(w.CreatedAt),
		UpdatedAt:   timestamppb.New(w.UpdatedAt),
	}.Build()
}
