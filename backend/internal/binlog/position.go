package binlog

// ComparePositions compares validated positions in the same source log family.
// It does not establish that either position exists or is a committed boundary.
func ComparePositions(a, b Position) (int, error) {
	return comparePositions(a, b)
}

// ExpectedServerUUID is the immutable identity which ReadRange verifies on
// its actual physical connection. It is not a claim of a successful read.
func (r *Reader) ExpectedServerUUID() string {
	if r == nil {
		return ""
	}
	return r.serverUUID
}
