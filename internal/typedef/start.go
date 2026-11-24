package typedef

// Starting work, step 2 (docs/plans/starting-work.md, part 1): a Type's
// optional start block says where its Items may be created and by whom.
//
// Phase B stub: parsed only. Nothing checks the block at load, and
// Admits and StartNotes have no behaviour yet.

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

// StartNote is a note, not a problem: it stops nothing. It says what
// registering the drafts would change about who may create Items of a
// Type, since the latest version's start block judges a create of every
// version (starting-work.md, 1.3). pli does not show it yet: it needs a
// WARNING severity in kernel.v1 (journal, Later).
type StartNote struct {
	TypeID  string
	Version int // the draft that would become the latest
	// Code is StartGoverns (the draft adds or changes the block, which
	// will govern the earlier versions) or StartLoosened (the draft drops
	// the block in force, which loosens them).
	Code string
	// Earlier are the versions it would govern or loosen, ascending:
	// registered and draft versions below it.
	Earlier []int
	Message string
}

// The codes of StartNote.
const (
	StartGoverns  = "START_GOVERNS"
	StartLoosened = "START_LOOSENED"
)

// StartNotes compares, for each Type ID, the start block in force now (the
// latest registered version's, or none) with the one that would be in
// force once the drafts are registered (the latest version among drafts
// and registered). A note comes only when that latest version is a draft
// not yet registered, there are earlier versions, and the two blocks
// differ. Phase B stub.
func StartNotes(drafts, registered []*Type) []StartNote { return nil }
