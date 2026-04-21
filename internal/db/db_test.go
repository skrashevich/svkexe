package db

import (
	"database/sql"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// --- User tests ---

func TestCreateAndGetUser(t *testing.T) {
	db := openTestDB(t)

	u := &User{ID: "u1", Email: "alice@example.com", DisplayName: "Alice", Role: "user"}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := db.GetUserByID("u1")
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Email != u.Email {
		t.Errorf("email: want %q got %q", u.Email, got.Email)
	}

	got2, err := db.GetUserByEmail("alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got2.ID != u.ID {
		t.Errorf("id: want %q got %q", u.ID, got2.ID)
	}
}

func TestGetUserByID_NotFound(t *testing.T) {
	db := openTestDB(t)
	_, err := db.GetUserByID("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestUpdateUser(t *testing.T) {
	db := openTestDB(t)
	u := &User{ID: "u2", Email: "bob@example.com", DisplayName: "Bob", Role: "user"}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u.Role = "admin"
	u.DisplayName = "Bob Admin"
	if err := db.UpdateUser(u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	got, _ := db.GetUserByID("u2")
	if got.Role != "admin" {
		t.Errorf("role: want admin got %q", got.Role)
	}
}

func TestDeleteUser(t *testing.T) {
	db := openTestDB(t)
	u := &User{ID: "u3", Email: "carol@example.com", DisplayName: "Carol", Role: "user"}
	_ = db.CreateUser(u)
	if err := db.DeleteUser("u3"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	_, err := db.GetUserByID("u3")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete, got %v", err)
	}
}

// --- Container tests ---

func TestCreateAndGetContainer(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner1", Email: "owner@example.com", Role: "user"})

	c := &Container{
		ID: "c1", Name: "my-box", OwnerID: "owner1",
		IncusName: "incus-c1", Status: "creating",
		CPULimit: 2, MemoryMB: 2048, DiskGB: 10,
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	got, err := db.GetContainerByID("c1")
	if err != nil {
		t.Fatalf("GetContainerByID: %v", err)
	}
	if got.OwnerID != "owner1" {
		t.Errorf("owner: want owner1 got %q", got.OwnerID)
	}
}

func TestListContainersByOwner(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner2", Email: "o2@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c2", Name: "box-a", OwnerID: "owner2", IncusName: "incus-c2", Status: "running"})
	_ = db.CreateContainer(&Container{ID: "c3", Name: "box-b", OwnerID: "owner2", IncusName: "incus-c3", Status: "stopped"})

	list, err := db.ListContainersByOwner("owner2")
	if err != nil {
		t.Fatalf("ListContainersByOwner: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("want 2 containers, got %d", len(list))
	}
}

func TestUpdateContainerStatus(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner3", Email: "o3@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c4", Name: "box-c", OwnerID: "owner3", IncusName: "incus-c4", Status: "creating"})

	if err := db.UpdateContainerStatus("c4", "running", "10.0.0.5"); err != nil {
		t.Fatalf("UpdateContainerStatus: %v", err)
	}
	got, _ := db.GetContainerByID("c4")
	if got.Status != "running" {
		t.Errorf("status: want running got %q", got.Status)
	}
	if got.IPAddress != "10.0.0.5" {
		t.Errorf("ip: want 10.0.0.5 got %q", got.IPAddress)
	}
}

func TestDeleteContainer(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner4", Email: "o4@example.com", Role: "user"})
	_ = db.CreateContainer(&Container{ID: "c5", Name: "box-d", OwnerID: "owner4", IncusName: "incus-c5", Status: "stopped"})

	if err := db.DeleteContainer("c5"); err != nil {
		t.Fatalf("DeleteContainer: %v", err)
	}
	_, err := db.GetContainerByID("c5")
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete, got %v", err)
	}
}

// --- API Key tests ---

var testEncKey = []byte("12345678901234567890123456789012") // 32 bytes

func TestCreateAndGetAPIKey(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner5", Email: "o5@example.com", Role: "user"})

	if err := db.CreateAPIKey("k1", "owner5", "openai", "sk-secret123", testEncKey); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	plain, err := db.GetAPIKeyPlaintext("k1", testEncKey)
	if err != nil {
		t.Fatalf("GetAPIKeyPlaintext: %v", err)
	}
	if plain != "sk-secret123" {
		t.Errorf("plaintext: want sk-secret123 got %q", plain)
	}
}

func TestListAPIKeysByOwner(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner6", Email: "o6@example.com", Role: "user"})
	_ = db.CreateAPIKey("k2", "owner6", "openai", "sk-a", testEncKey)
	_ = db.CreateAPIKey("k3", "owner6", "anthropic", "sk-b", testEncKey)

	keys, err := db.ListAPIKeysByOwner("owner6")
	if err != nil {
		t.Fatalf("ListAPIKeysByOwner: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("want 2 keys, got %d", len(keys))
	}
}

func TestDeleteAPIKey(t *testing.T) {
	db := openTestDB(t)
	_ = db.CreateUser(&User{ID: "owner7", Email: "o7@example.com", Role: "user"})
	_ = db.CreateAPIKey("k4", "owner7", "openai", "sk-x", testEncKey)

	if err := db.DeleteAPIKey("k4"); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	_, err := db.GetAPIKeyPlaintext("k4", testEncKey)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes AES-128
	plaintext := []byte("super-secret-value")

	encrypted, err := encryptAESGCM(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := decryptAESGCM(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip: want %q got %q", plaintext, decrypted)
	}
}
