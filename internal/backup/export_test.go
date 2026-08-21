package backup

// SetAfterBlock registers fn to run after each stored block. Tests use it to
// keep a capture in progress until they cancel.
func (e *Engine) SetAfterBlock(fn func()) {
	e.afterBlock = fn
}
