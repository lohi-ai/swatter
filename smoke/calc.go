// Package smoke is a throwaway fixture used to smoke-test Swatter's
// multi-round PR review (resolve-stale + dedup). It is NOT meant to be merged.
package smoke

// SafeDivide should guard against a zero divisor, but currently doesn't.
func SafeDivide(a, b int) int {
	return a / b // integer divide-by-zero panics when b == 0
}

// FirstByte returns the first byte of s.
func FirstByte(s string) byte {
	return s[0] // index out of range panic when s is empty
}
