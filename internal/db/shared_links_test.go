package db

import (
	"database/sql"
	"testing"
	"time"
)

func TestCreateSharedLink(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	link, err := db.CreateSharedLink("c1", "u1", nil)
	if err != nil {
		t.Fatalf("CreateSharedLink: %v", err)
	}
	if link.Token == "" {
		t.Error("expected non-empty token")
	}
	if link.ContainerID != "c1" {
		t.Errorf("container_id: want c1 got %q", link.ContainerID)
	}
	if link.ExpiresAt != nil {
		t.Error("expected nil ExpiresAt")
	}
}

func TestGetSharedLinkByToken(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	link, err := db.CreateSharedLink("c1", "u1", nil)
	if err != nil {
		t.Fatalf("CreateSharedLink: %v", err)
	}

	got, err := db.GetSharedLinkByToken(link.Token)
	if err != nil {
		t.Fatalf("GetSharedLinkByToken: %v", err)
	}
	if got.ID != link.ID {
		t.Errorf("id: want %q got %q", link.ID, got.ID)
	}
}

func TestGetSharedLinkByToken_Expired(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	past := time.Now().UTC().Add(-1 * time.Hour)
	link, err := db.CreateSharedLink("c1", "u1", &past)
	if err != nil {
		t.Fatalf("CreateSharedLink: %v", err)
	}

	_, err = db.GetSharedLinkByToken(link.Token)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows for expired link, got %v", err)
	}
}

func TestGetSharedLinkByToken_FutureExpiry(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	future := time.Now().Add(24 * time.Hour)
	link, err := db.CreateSharedLink("c1", "u1", &future)
	if err != nil {
		t.Fatalf("CreateSharedLink: %v", err)
	}

	got, err := db.GetSharedLinkByToken(link.Token)
	if err != nil {
		t.Fatalf("GetSharedLinkByToken: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Error("expected non-nil ExpiresAt")
	}
}

func TestListSharedLinksByContainer(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	_, _ = db.CreateSharedLink("c1", "u1", nil)
	_, _ = db.CreateSharedLink("c1", "u1", nil)

	links, err := db.ListSharedLinksByContainer("c1")
	if err != nil {
		t.Fatalf("ListSharedLinksByContainer: %v", err)
	}
	if len(links) != 2 {
		t.Errorf("want 2 links, got %d", len(links))
	}
}

func TestDeleteSharedLink(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	link, _ := db.CreateSharedLink("c1", "u1", nil)

	if err := db.DeleteSharedLink(link.ID); err != nil {
		t.Fatalf("DeleteSharedLink: %v", err)
	}
	_, err := db.GetSharedLinkByToken(link.Token)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestDeleteSharedLinkByToken(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "u1", Email: "alice@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c1", Name: "box", OwnerID: "u1", IncusName: "incus-c1", Status: "running"})

	link, _ := db.CreateSharedLink("c1", "u1", nil)

	if err := db.DeleteSharedLinkByToken(link.Token); err != nil {
		t.Fatalf("DeleteSharedLinkByToken: %v", err)
	}
	_, err := db.GetSharedLinkByToken(link.Token)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete by token, got %v", err)
	}
}
