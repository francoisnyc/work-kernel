package typedef

import "errors"

// Source is one draft Type file: its name (a path, say) and its YAML.
type Source struct {
	Name    string
	Content []byte
}

// Check runs every stage on the drafts, as ParseDir does on a folder, with
// the registered Type versions (their stored JSON definitions) loaded beside
// them (Ventoux V2b). Stub: not implemented yet.
func Check(sources []Source, registered [][]byte) ([]*Type, error) {
	return nil, errors.New("typedef.Check: not implemented")
}
