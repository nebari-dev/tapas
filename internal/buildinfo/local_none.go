//go:build !local

package buildinfo

// Local says whether this binary was built with the tag local: not
// this one, which carries everything Tapas runs on.
const Local = false
