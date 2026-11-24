package typedef

// Starting work, step 2 (docs/plans/starting-work.md, part 1): a Type's
// optional start block says where its Items may be created and by whom.
//
// Phase B stub: parsed only. Nothing checks the block at load, and
// Admits has no behaviour yet.

// Start is a Type's start block. In lists container Type IDs (any version
// of each), or StartTop for no container; absent means anywhere. Allowed
// lists who may create one: role:<name>, principal:user:<id>, field:<id>
// (a principal field of the container), container_creator, or nobody;
// absent or empty means anyone who sees the container. Only the latest
// registered version's block judges a create, of any version.
type Start struct {
	In      []string `yaml:"in" json:"in,omitempty"`
	Allowed []string `yaml:"allowed" json:"allowed,omitempty"`
}

// StartTop in start.in means "at the top level, in no container". A Type
// may not be named top (RESERVED_NAME), so the word means one thing.
const StartTop = "top"

// Admits reports whether start.in lets an Item be created in a container
// of Type containerType; "" is the top level. A nil block admits anywhere.
// Phase B stub.
func (s *Start) Admits(containerType string) bool { return true }
