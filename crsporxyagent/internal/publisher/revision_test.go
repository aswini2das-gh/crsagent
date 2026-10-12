package publisher

import (
	"testing"
)

// TestRevisionTracker_Next verifies that sequential calls return 1, 2, 3...
func TestRevisionTracker_Next(t *testing.T) {
	rt := NewRevisionTracker()

	for i := int64(1); i <= 10; i++ {
		got := rt.Next("node-1")
		if got != i {
			t.Errorf("call %d: expected revision %d, got %d", i, i, got)
		}
	}
}

// TestRevisionTracker_MultipleNodes verifies that different node UIDs
// have independent counters.
func TestRevisionTracker_MultipleNodes(t *testing.T) {
	rt := NewRevisionTracker()

	// Advance node-a to revision 3.
	rt.Next("node-a")
	rt.Next("node-a")
	r3 := rt.Next("node-a")
	if r3 != 3 {
		t.Errorf("expected node-a revision=3, got %d", r3)
	}

	// node-b should start at 1.
	rb1 := rt.Next("node-b")
	if rb1 != 1 {
		t.Errorf("expected node-b revision=1, got %d", rb1)
	}

	// node-a should continue at 4.
	r4 := rt.Next("node-a")
	if r4 != 4 {
		t.Errorf("expected node-a revision=4, got %d", r4)
	}

	// node-b should be at 2.
	rb2 := rt.Next("node-b")
	if rb2 != 2 {
		t.Errorf("expected node-b revision=2, got %d", rb2)
	}
}

// TestRevisionTracker_Remove verifies that after Remove, Next starts
// at 1 again for the removed node UID.
func TestRevisionTracker_Remove(t *testing.T) {
	rt := NewRevisionTracker()

	// Advance to revision 5.
	for i := 0; i < 5; i++ {
		rt.Next("node-x")
	}

	current := rt.Current("node-x")
	if current != 5 {
		t.Errorf("expected current revision=5, got %d", current)
	}

	// Remove and verify next starts at 1.
	rt.Remove("node-x")

	current = rt.Current("node-x")
	if current != 0 {
		t.Errorf("expected current revision=0 after remove, got %d", current)
	}

	got := rt.Next("node-x")
	if got != 1 {
		t.Errorf("expected revision=1 after remove, got %d", got)
	}
}

// TestRevisionTracker_RemoveNonexistent verifies that removing a node
// that was never tracked does not panic.
func TestRevisionTracker_RemoveNonexistent(t *testing.T) {
	rt := NewRevisionTracker()

	// Should not panic.
	rt.Remove("never-tracked")

	got := rt.Next("never-tracked")
	if got != 1 {
		t.Errorf("expected revision=1 for previously removed non-existent node, got %d", got)
	}
}
