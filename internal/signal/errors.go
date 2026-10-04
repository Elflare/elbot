package signal

import "errors"

// filterErrors removes only matching leaves, preserving unrelated joined errors.
// Wrappers are retained when unchanged; partially filtered trees keep the actual
// remaining causes rather than a wrapper still reporting cancelled operations.
func filterErrors(err error, expected func(error) bool) (error, bool) {
	if err == nil {
		return nil, false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error
		changed := false
		for _, child := range joined.Unwrap() {
			remaining, removed := filterErrors(child, expected)
			changed = changed || removed
			if remaining != nil {
				kept = append(kept, remaining)
			}
		}
		if changed {
			return errors.Join(kept...), true
		}
		return err, false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && wrapped.Unwrap() != nil {
		remaining, changed := filterErrors(wrapped.Unwrap(), expected)
		if changed {
			return remaining, true
		}
		return err, false
	}
	if expected(err) {
		return nil, true
	}
	return err, false
}
