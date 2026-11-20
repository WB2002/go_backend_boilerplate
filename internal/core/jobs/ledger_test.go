package jobs

import "testing"

// NewTestLedger constructs a new in-memory test ledger.
func NewTestLedger(t testing.TB) Ledger {
	if t != nil {
		t.Helper()
	}
	return NewMemoryLedger()
}
