package featureflags

import (
	"testing"
)

func TestRegisterAndLookup(t *testing.T) {
	f := Register(Flag{Name: "test-flag-1", Description: "d", Default: true})
	if f.Name != "test-flag-1" {
		t.Fatalf("unexpected: %+v", f)
	}
	got, ok := Lookup("test-flag-1")
	if !ok || got.Default != true {
		t.Fatalf("lookup: %+v ok=%v", got, ok)
	}
	if Known("nope") {
		t.Fatal("should be unknown")
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("duplicate Register should panic")
		}
	}()
	Register(Flag{Name: "test-flag-1", Default: false})
}
