// Package smoke is a throwaway fixture used to smoke-test Swatter's
// multi-round PR review (resolve-stale + dedup). It is NOT meant to be merged.
package smoke

// SafeDivide guards against a zero divisor, returning 0 in that case.
func SafeDivide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}

// FirstByte returns the first byte of s.
func FirstByte(s string) byte {
	return s[0] // index out of range panic when s is empty
}
