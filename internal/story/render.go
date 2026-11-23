package story

import (
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Renderer writes events and side effects as timeline lines, in the
// grammar of the package documentation.
type Renderer struct {
	Day1    time.Time
	People  map[string]string    // principal -> the person's name
	Items   map[uuid.UUID]string // Item -> its name
	Hidden  map[string][]string  // Type ID -> fields whose values are left out
	Catalog *contracts.Catalog   // a call's inputs and outputs take their types from it
}

// Event renders one event of an Item of the Type ty (whose fields say how
// each value is written). own lists the fields the step that wrote it sent
// itself, left out of its values.
func (r Renderer) Event(ty *typedef.Type, ev itemservice.Event, own []string) Line {
	_, _, _ = ty, ev, own
	return Line{} // stub
}

// Call renders a side effect a fake was asked for.
func (r Renderer) Call(c Call) Line {
	_ = c
	return Line{} // stub
}
