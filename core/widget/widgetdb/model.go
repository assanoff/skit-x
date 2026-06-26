package widgetdb

import (
	"time"

	"github.com/google/uuid"

	"github.com/assanoff/skit-x/core/widget"
)

// dbWidget is the database representation of a widget.
type dbWidget struct {
	ID          uuid.UUID `db:"id"`
	Name        string    `db:"name"`
	Description string    `db:"description"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

func toDBWidget(w widget.Widget) dbWidget {
	return dbWidget{
		ID:          w.ID,
		Name:        w.Name,
		Description: w.Description,
		CreatedAt:   w.CreatedAt.UTC(),
		UpdatedAt:   w.UpdatedAt.UTC(),
	}
}

func toCoreWidget(r dbWidget) widget.Widget {
	return widget.Widget{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		CreatedAt:   r.CreatedAt.In(time.UTC),
		UpdatedAt:   r.UpdatedAt.In(time.UTC),
	}
}

func toCoreWidgets(rows []dbWidget) []widget.Widget {
	out := make([]widget.Widget, len(rows))
	for i, r := range rows {
		out[i] = toCoreWidget(r)
	}
	return out
}
