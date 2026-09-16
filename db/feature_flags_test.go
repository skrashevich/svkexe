package db

import (
	"context"
	"testing"
)

func TestFeatureFlagOverrides(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()

	got, err := db.GetAllFeatureFlagOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}

	if err := db.SetFeatureFlagOverride(ctx, "foo", `true`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFeatureFlagOverride(ctx, "bar", `"hello"`); err != nil {
		t.Fatal(err)
	}
	// Upsert.
	if err := db.SetFeatureFlagOverride(ctx, "foo", `false`); err != nil {
		t.Fatal(err)
	}

	got, err = db.GetAllFeatureFlagOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["foo"] != "false" || got["bar"] != `"hello"` || len(got) != 2 {
		t.Fatalf("unexpected: %v", got)
	}

	if err := db.DeleteFeatureFlagOverride(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAllFeatureFlagOverrides(ctx)
	if _, ok := got["foo"]; ok {
		t.Fatalf("foo not deleted: %v", got)
	}
}
