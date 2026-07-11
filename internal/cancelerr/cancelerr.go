// Package cancelerr classifies errors returned during cooperative shutdown.
package cancelerr

import "context"

// Only reports whether err is nil or consists solely of context cancellation,
// including wrapped and joined cancellation errors.
func Only(err error) bool {
	if err == nil {
		return true
	}
	if err == context.Canceled {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !Only(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		child := wrapped.Unwrap()
		return child != nil && Only(child)
	}
	return false
}
