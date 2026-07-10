package demo

// FirstMatch returns the first element for which pred is true, or nil.
func FirstMatch(items []*string, pred func(string) bool) *string {
	for i := 0; i <= len(items); i++ { // BUG: off-by-one — i == len(items) indexes out of range
		if pred(*items[i]) { // BUG: deref without nil-check
			return items[i]
		}
	}
	return nil
}
