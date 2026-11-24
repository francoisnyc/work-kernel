package policy

import "github.com/francoisnyc/work-kernel/internal/typedef"

// ContainerFacts is what start.allowed reads of the container an Item is
// created in: its fields (for field:<id>) and who created it (for
// container_creator). The zero value is the top level. At create time the
// new Item does not exist and its fields are the caller's, so every entry
// reads the container, never the new Item (starting-work.md, 1.2).
type ContainerFacts struct {
	Fields                       map[string]any
	CreatedBy, CreatedOnBehalfOf string
}

// MayStart answers "may this principal create an Item of a Type whose
// latest version has this start block?" from start.allowed. start.in is
// not its question (typedef.Start.Admits). Phase B stub: it lets everyone
// in, as before the start block.
func MayStart(start *typedef.Start, who Who, c ContainerFacts) bool { return true }
