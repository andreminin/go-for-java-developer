// Package examples collects tiny reusable snippets referenced by docs.
//
// In Java terms this is a small utility library (like Guava snippets):
// public API lives here, learning demos live under internal/.
package examples

// Counter is a minimal value type used by docs to show struct vs class.
type Counter struct {
	N int
}

// Inc increments the counter. Pointer receiver mutates the original,
// like a non-static Java method mutating `this`.
func (c *Counter) Inc() { c.N++ }
