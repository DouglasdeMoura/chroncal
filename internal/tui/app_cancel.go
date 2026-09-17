package tui

import "context"

// The TUI blocks the calendar list and the account manager while a sync or
// an account discovery runs. A server that accepts the connection and never
// answers then holds the screen for the whole budget. esc is the way out:
// it cancels the operation, and the operation reports the cancellation
// through its own finished message.
//
// A create, an import, and a reconcile stay outside this contract. Those
// operations write rows, and a cancel in the middle leaves a partial link.

// beginCancellableOp opens the parent context of an operation that esc can
// stop. The operation derives its own budget from the returned context. The
// model keeps the cancel function, so the esc handler can reach it.
func (m Model) beginCancellableOp() (Model, context.Context) {
	m = m.endCancellableOp()
	ctx, cancel := context.WithCancel(context.Background())
	m.opCancel = cancel
	return m, ctx
}

// endCancellableOp releases the parent context of the finished operation.
// The terminal message handler of each cancellable operation calls it. A
// second call does nothing.
func (m Model) endCancellableOp() Model {
	if m.opCancel != nil {
		m.opCancel()
		m.opCancel = nil
	}
	m.opCancelled = false
	return m
}

// cancelRunningOp stops the running operation. It reports whether it stopped
// one. The operation then fails with a cancelled context, and its finish
// handler maps that to the "cancelled" status line.
func (m Model) cancelRunningOp() (Model, bool) {
	if !m.syncing || m.opCancel == nil {
		return m, false
	}
	m.opCancel()
	m.opCancel = nil
	m.opCancelled = true
	m.statusToken++
	m.syncStatus = "Cancelling…"
	return m, true
}
