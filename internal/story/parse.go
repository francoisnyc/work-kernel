package story

// Parse reads a story file. source names it in problems. Every problem of
// the file is reported at once, as Problems, each with its line.
func Parse(source string, data []byte) (*Story, error) {
	_, _ = source, data
	return nil, errNotBuilt
}
