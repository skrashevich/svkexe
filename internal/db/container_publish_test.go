package db

import "testing"

// Existing VMs must keep working after the upgrade and must not become
// reachable without a session just because the columns appeared.
func TestContainerPublishDefaults(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.AppPort != DefaultAppPort {
		t.Errorf("AppPort=%d, want %d", c.AppPort, DefaultAppPort)
	}
	if c.AppPublic {
		t.Error("a new VM must default to private")
	}
}

func TestUpdateContainerPublish(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateContainerPublish("vm", 8080, true); err != nil {
		t.Fatal(err)
	}
	for _, load := range []func() (*Container, error){
		func() (*Container, error) { return database.GetContainerByID("vm") },
		func() (*Container, error) { return database.GetContainerByName("box", "owner") },
		func() (*Container, error) { return database.GetContainerByNameOnly("box") },
	} {
		c, err := load()
		if err != nil {
			t.Fatal(err)
		}
		if c.AppPort != 8080 || !c.AppPublic {
			t.Fatalf("settings not loaded: port=%d public=%v", c.AppPort, c.AppPublic)
		}
	}
	owned, err := database.ListContainersByOwner("owner")
	if err != nil || len(owned) != 1 || owned[0].AppPort != 8080 || !owned[0].AppPublic {
		t.Fatalf("list lost settings: %+v err=%v", owned, err)
	}

	// Turning a VM back to private must take effect immediately.
	if err := database.UpdateContainerPublish("vm", 3000, false); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.AppPort != 3000 || c.AppPublic {
		t.Fatalf("private switch lost: port=%d public=%v", c.AppPort, c.AppPublic)
	}
}

func TestUpdateContainerPublishRejectsInvalidPort(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{0, -1, 65536, AgentPort} {
		if err := database.UpdateContainerPublish("vm", port, false); err == nil {
			t.Errorf("port %d accepted", port)
		}
	}
}

// A database created before the columns existed must migrate without losing rows.
func TestContainerPublishMigrationPreservesRows(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateContainerPublish("vm", 8080, true); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"app_port", "app_public"} {
		if _, err := database.Exec("ALTER TABLE containers DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := database.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "box" {
		t.Fatalf("migration lost the row: %+v", c)
	}
	if c.AppPort != DefaultAppPort || c.AppPublic {
		t.Fatalf("migrated VM must fall back to a private default: port=%d public=%v", c.AppPort, c.AppPublic)
	}
}
