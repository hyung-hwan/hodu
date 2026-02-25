package hodu

import "testing"

func TestAtomGetReturnsZeroValueBeforeSet(t *testing.T) {
	var a Atom[int]
	var got int

	got = a.Get()
	if got != 0 {
		t.Fatalf("expected zero value before Set, got %d", got)
	}
}

func TestAtomSetAndGet(t *testing.T) {
	var a Atom[string]
	var got string

	a.Set("alpha")
	got = a.Get()
	if got != "alpha" {
		t.Fatalf("expected alpha, got %q", got)
	}
}

func TestAtomSetOverwritesPreviousValue(t *testing.T) {
	var a Atom[bool]
	var got bool

	a.Set(false)
	a.Set(true)
	got = a.Get()
	if !got {
		t.Fatalf("expected overwritten value true, got %v", got)
	}
}
