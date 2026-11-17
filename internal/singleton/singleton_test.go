//go:build !windows

package singleton

import (
	"errors"
	"testing"
)

func TestASecondCopyIsRefusedUntilTheFirstLetsGo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := Acquire("test-role", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire("test-role", 0); !errors.Is(err, ErrHeld) {
		t.Fatalf("second copy must be refused, got %v", err)
	}
	release()
	again, err := Acquire("test-role", 0)
	if err != nil {
		t.Fatalf("lock should be free after release: %v", err)
	}
	again()
}
