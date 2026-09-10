package db

import (
	"database/sql"
	"errors"
	"testing"
)

// withoutForeignKeys reproduces the condition every query outside Open faces in
// production: `PRAGMA foreign_keys=ON` is set on whichever pooled connection
// Open happened to use, so any other connection runs with enforcement off and
// ON DELETE CASCADE silently does nothing. Deletion tests run under it so they
// prove our own SQL removes the rows rather than passing on a cascade that will
// not be there. The in-memory pool is a single connection, which is what makes
// turning it off here deterministic.
func withoutForeignKeys(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	var on int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if on != 0 {
		t.Fatal("foreign key enforcement is still on, the test would prove nothing")
	}
}

// seedAliasContainer creates an owner and a container to hang aliases off.
func seedAliasContainer(t *testing.T, db *DB, id string) *Container {
	t.Helper()
	owner := &User{ID: "owner-" + id, Email: id + "@example.com", Role: "user"}
	if err := db.CreateUser(owner); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	c := &Container{
		ID:        id,
		Name:      id,
		OwnerID:   owner.ID,
		IncusName: "svkexe-" + owner.ID + "-" + id,
		Status:    "running",
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	return c
}

func TestValidAliasHostname(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		domain   string
		want     string
		wantErr  bool
	}{
		{name: "plain", hostname: "app.example.org", domain: "svkexe.dev", want: "app.example.org"},
		{name: "normalises case and trailing dot", hostname: "  App.Example.org.  ", domain: "svkexe.dev", want: "app.example.org"},
		{name: "deep subdomain", hostname: "a.b.c.example.org", domain: "svkexe.dev", want: "a.b.c.example.org"},
		{name: "hyphens inside a label", hostname: "my-app.example.org", domain: "svkexe.dev", want: "my-app.example.org"},
		{name: "empty", hostname: "", domain: "svkexe.dev", wantErr: true},
		{name: "whitespace only", hostname: "   ", domain: "svkexe.dev", wantErr: true},
		{name: "no dot", hostname: "localhost", domain: "svkexe.dev", wantErr: true},
		{name: "empty label", hostname: "app..example.org", domain: "svkexe.dev", wantErr: true},
		{name: "leading hyphen", hostname: "-app.example.org", domain: "svkexe.dev", wantErr: true},
		{name: "trailing hyphen", hostname: "app-.example.org", domain: "svkexe.dev", wantErr: true},
		{name: "underscore", hostname: "my_app.example.org", domain: "svkexe.dev", wantErr: true},
		{name: "label too long", hostname: repeat("a", 64) + ".example.org", domain: "svkexe.dev", wantErr: true},
		{name: "hostname too long", hostname: longHostname(), domain: "svkexe.dev", wantErr: true},
		{name: "numeric last label", hostname: "203.0.113.9", domain: "svkexe.dev", wantErr: true},
		{name: "the gateway domain itself", hostname: "svkexe.dev", domain: "svkexe.dev", wantErr: true},
		{name: "under the gateway domain", hostname: "vm.svkexe.dev", domain: "svkexe.dev", wantErr: true},
		{name: "under the gateway domain, mixed case", hostname: "VM.SvkExe.Dev", domain: "svkexe.dev", wantErr: true},
		{name: "similar but not under the gateway domain", hostname: "notsvkexe.dev", domain: "svkexe.dev", want: "notsvkexe.dev"},
		{name: "no domain configured accepts anything valid", hostname: "app.example.org", domain: "", want: "app.example.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidAliasHostname(tt.hostname, tt.domain)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("want %q got %q", tt.want, got)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

// longHostname builds a name over the 253-character limit out of legal labels,
// so the length check is what rejects it and not the label rules.
func longHostname() string {
	label := repeat("a", 60)
	h := ""
	for range 5 {
		h += label + "."
	}
	return h + "example.org"
}

func TestCreateAndListAliases(t *testing.T) {
	db := openTestDB(t)
	c := seedAliasContainer(t, db, "vm1")

	a, err := db.CreateContainerAlias(c.ID, "app.example.org")
	if err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}
	if a.ID == "" {
		t.Error("alias has no ID")
	}
	if a.Verified {
		t.Error("a freshly created alias must start unverified")
	}
	if a.VerifiedAt != nil {
		t.Error("a freshly created alias must have no verified_at")
	}
	if a.CreatedAt.IsZero() {
		t.Error("created_at was not populated")
	}

	if _, err := db.CreateContainerAlias(c.ID, "www.example.org"); err != nil {
		t.Fatalf("second alias: %v", err)
	}

	list, err := db.ListAliasesByContainer(c.ID)
	if err != nil {
		t.Fatalf("ListAliasesByContainer: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 aliases, got %d", len(list))
	}
}

// An unverified claim is a request, not a reservation. If it were exclusive,
// anyone could park a domain they do not control and hold it against its real
// owner — the very thing the DNS check exists to prevent.
func TestPendingClaimDoesNotReserveAHostname(t *testing.T) {
	db := openTestDB(t)
	squatter := seedAliasContainer(t, db, "vm1")
	realOwner := seedAliasContainer(t, db, "vm2")

	if _, err := db.CreateContainerAlias(squatter.ID, "app.example.org"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// The real owner can still ask for their own domain.
	theirs, err := db.CreateContainerAlias(realOwner.ID, "app.example.org")
	if err != nil {
		t.Fatalf("second pending claim was refused: %v", err)
	}

	// Whoever's DNS actually points here wins the name.
	if err := db.SetAliasVerification(theirs.ID, true, ""); err != nil {
		t.Fatalf("verify the real owner's claim: %v", err)
	}
	got, err := db.GetVerifiedAliasByHostname("app.example.org")
	if err != nil {
		t.Fatalf("GetVerifiedAliasByHostname: %v", err)
	}
	if got.ContainerID != realOwner.ID {
		t.Errorf("hostname resolved to %q, want the VM that verified it", got.ContainerID)
	}
}

// Once a hostname is verified it belongs to that VM: nobody else may claim it,
// and a pending claim elsewhere can no longer be verified into a second owner.
func TestVerifiedHostnameIsExclusive(t *testing.T) {
	db := openTestDB(t)
	owner := seedAliasContainer(t, db, "vm1")
	other := seedAliasContainer(t, db, "vm2")

	mine, err := db.CreateContainerAlias(owner.ID, "app.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetAliasVerification(mine.ID, true, ""); err != nil {
		t.Fatal(err)
	}

	// A fresh claim is refused outright.
	if _, err := db.CreateContainerAlias(other.ID, "app.example.org"); !errors.Is(err, ErrAliasHostnameTaken) {
		t.Errorf("claiming a verified hostname: want ErrAliasHostnameTaken, got %v", err)
	}
	if got, err := db.GetVerifiedAliasByHostname("app.example.org"); err != nil || got.ContainerID != owner.ID {
		t.Errorf("the hostname changed hands: %+v, %v", got, err)
	}

	// Releasing it lets the other VM take it.
	if err := db.DeleteContainerAlias(mine.ID); err != nil {
		t.Fatal(err)
	}
	theirs, err := db.CreateContainerAlias(other.ID, "app.example.org")
	if err != nil {
		t.Fatalf("a released hostname must be claimable: %v", err)
	}
	if err := db.SetAliasVerification(theirs.ID, true, ""); err != nil {
		t.Errorf("a released hostname must be verifiable by the other VM: %v", err)
	}
}

// A pending claim on somebody else's domain must not survive as an option on
// the name. The DNS check proves a hostname points at THIS GATEWAY, not who
// owns it, so on a shared gateway anyone can satisfy it — a parked claim would
// simply wait for the rightful owner's flag to drop (a transient re-check
// failure is enough) and then take the name, and the traffic, under a valid
// certificate. Verification destroys the competition instead.
func TestVerifyingDestroysCompetingPendingClaims(t *testing.T) {
	db := openTestDB(t)
	victim := seedAliasContainer(t, db, "vm1")
	attacker := seedAliasContainer(t, db, "vm2")
	bystander := seedAliasContainer(t, db, "vm3")

	parked, err := db.CreateContainerAlias(attacker.ID, "app.example.org")
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated domain on the same VM must be left alone.
	untouched, err := db.CreateContainerAlias(attacker.ID, "other.example.org")
	if err != nil {
		t.Fatal(err)
	}
	// A pending claim on a different hostname elsewhere, likewise.
	elsewhere, err := db.CreateContainerAlias(bystander.ID, "third.example.org")
	if err != nil {
		t.Fatal(err)
	}

	mine, err := db.CreateContainerAlias(victim.ID, "app.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetAliasVerification(mine.ID, true, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}

	if _, err := db.GetAliasByID(parked.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Error("a competing pending claim survived verification and can still take the name later")
	}
	for _, keep := range []*ContainerAlias{untouched, elsewhere} {
		if _, err := db.GetAliasByID(keep.ID); err != nil {
			t.Errorf("verification removed an unrelated claim %s: %v", keep.Hostname, err)
		}
	}
	// The owner's own row obviously stays.
	if got, err := db.GetAliasByID(mine.ID); err != nil || !got.Verified {
		t.Errorf("the verified alias itself was lost: %+v, %v", got, err)
	}
}

// Clearing the flag must not resurrect anybody: the name simply becomes free.
func TestClearingVerificationDoesNotRestoreCompetingClaims(t *testing.T) {
	db := openTestDB(t)
	victim := seedAliasContainer(t, db, "vm1")
	attacker := seedAliasContainer(t, db, "vm2")

	if _, err := db.CreateContainerAlias(attacker.ID, "app.example.org"); err != nil {
		t.Fatal(err)
	}
	mine, err := db.CreateContainerAlias(victim.ID, "app.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetAliasVerification(mine.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// The transient failure the attacker was waiting for.
	if err := db.SetAliasVerification(mine.ID, false, "DNS hiccup"); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListAliasesByContainer(attacker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("the attacker still holds a claim on the name: %+v", rows)
	}
	// Nothing routes until somebody proves the name again.
	if _, err := db.GetVerifiedAliasByHostname("app.example.org"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("hostname still routes after its verification was cleared: %v", err)
	}
}

// One VM lists a hostname once, so its card cannot show the same domain twice.
func TestCreateAliasRejectsDuplicateOnTheSameVM(t *testing.T) {
	db := openTestDB(t)
	c := seedAliasContainer(t, db, "vm1")

	if _, err := db.CreateContainerAlias(c.ID, "app.example.org"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Told it is their own domain, not that somebody else holds it.
	if _, err := db.CreateContainerAlias(c.ID, "app.example.org"); !errors.Is(err, ErrAliasAlreadyOnVM) {
		t.Fatalf("want ErrAliasAlreadyOnVM on re-claim, got %v", err)
	}
}

func TestCreateAliasEnforcesPerContainerLimit(t *testing.T) {
	db := openTestDB(t)
	c := seedAliasContainer(t, db, "vm1")

	for i := range MaxAliasesPerContainer {
		host := string(rune('a'+i)) + ".example.org"
		if _, err := db.CreateContainerAlias(c.ID, host); err != nil {
			t.Fatalf("alias %d: %v", i, err)
		}
	}
	if _, err := db.CreateContainerAlias(c.ID, "one-too-many.example.org"); err == nil {
		t.Fatal("want an error past the per-VM limit, got nil")
	}
}

func TestGetVerifiedAliasByHostname(t *testing.T) {
	db := openTestDB(t)
	c := seedAliasContainer(t, db, "vm1")

	a, err := db.CreateContainerAlias(c.ID, "app.example.org")
	if err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}

	// An unverified alias is invisible to routing.
	if _, err := db.GetVerifiedAliasByHostname("app.example.org"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unverified alias must not resolve, got %v", err)
	}

	if err := db.SetAliasVerification(a.ID, true, ""); err != nil {
		t.Fatalf("SetAliasVerification: %v", err)
	}

	got, err := db.GetVerifiedAliasByHostname("app.example.org")
	if err != nil {
		t.Fatalf("GetVerifiedAliasByHostname: %v", err)
	}
	if got.ContainerID != c.ID {
		t.Errorf("container: want %q got %q", c.ID, got.ContainerID)
	}
	if got.VerifiedAt == nil {
		t.Error("verified_at was not stamped")
	}

	// Host headers arrive in whatever case the client typed, and an FQDN may
	// carry a trailing dot; both must still resolve.
	if _, err := db.GetVerifiedAliasByHostname("APP.Example.ORG."); err != nil {
		t.Errorf("lookup must normalise the hostname: %v", err)
	}

	if _, err := db.GetVerifiedAliasByHostname("unknown.example.org"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want sql.ErrNoRows, got %v", err)
	}
}

func TestSetAliasVerificationClearsAndRecordsFailure(t *testing.T) {
	db := openTestDB(t)
	c := seedAliasContainer(t, db, "vm1")
	a, err := db.CreateContainerAlias(c.ID, "app.example.org")
	if err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}

	if err := db.SetAliasVerification(a.ID, true, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// A name that stopped pointing here must stop being routed.
	if err := db.SetAliasVerification(a.ID, false, "resolves to 203.0.113.9"); err != nil {
		t.Fatalf("unverify: %v", err)
	}

	got, err := db.GetAliasByID(a.ID)
	if err != nil {
		t.Fatalf("GetAliasByID: %v", err)
	}
	if got.Verified {
		t.Error("alias is still verified after a failed re-check")
	}
	if got.LastError != "resolves to 203.0.113.9" {
		t.Errorf("last_error: want the failure reason, got %q", got.LastError)
	}
	if _, err := db.GetVerifiedAliasByHostname("app.example.org"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("an unverified alias must stop resolving, got %v", err)
	}

	// A later success must clear the stale explanation.
	if err := db.SetAliasVerification(a.ID, true, ""); err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	got, err = db.GetAliasByID(a.ID)
	if err != nil {
		t.Fatalf("GetAliasByID: %v", err)
	}
	if got.LastError != "" {
		t.Errorf("last_error must be cleared on success, got %q", got.LastError)
	}
}

func TestDeleteContainerAlias(t *testing.T) {
	db := openTestDB(t)
	c := seedAliasContainer(t, db, "vm1")
	a, err := db.CreateContainerAlias(c.ID, "app.example.org")
	if err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}

	if err := db.DeleteContainerAlias(a.ID); err != nil {
		t.Fatalf("DeleteContainerAlias: %v", err)
	}
	if _, err := db.GetAliasByID(a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want sql.ErrNoRows, got %v", err)
	}
	// Releasing the name must let it be claimed again.
	if _, err := db.CreateContainerAlias(c.ID, "app.example.org"); err != nil {
		t.Fatalf("re-claim after delete: %v", err)
	}
}

func TestDeleteContainerRemovesItsAliases(t *testing.T) {
	db := openTestDB(t)
	withoutForeignKeys(t, db)
	c := seedAliasContainer(t, db, "vm1")
	other := seedAliasContainer(t, db, "vm2")

	if _, err := db.CreateContainerAlias(c.ID, "app.example.org"); err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}
	if _, err := db.CreateContainerAlias(other.ID, "keep.example.org"); err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}

	if err := db.DeleteContainer(c.ID); err != nil {
		t.Fatalf("DeleteContainer: %v", err)
	}

	list, err := db.ListAliasesByContainer(c.ID)
	if err != nil {
		t.Fatalf("ListAliasesByContainer: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("want no aliases after the VM is deleted, got %d", len(list))
	}
	// A freed hostname must be claimable by another VM.
	if _, err := db.CreateContainerAlias(other.ID, "app.example.org"); err != nil {
		t.Fatalf("re-claim a deleted VM's hostname: %v", err)
	}
	// Deleting one VM must not touch another's aliases.
	kept, err := db.ListAliasesByContainer(other.ID)
	if err != nil {
		t.Fatalf("ListAliasesByContainer: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("want the other VM to keep its aliases, got %d", len(kept))
	}
}

// Deleting the owner takes their VMs, so it has to take the hostnames pointed
// at them too. A surviving row keeps answering the certificate check for a
// domain no account owns, and holds the name against everyone else forever.
func TestDeleteUserRemovesItsAliases(t *testing.T) {
	db := openTestDB(t)
	withoutForeignKeys(t, db)
	doomed := seedAliasContainer(t, db, "vm1")
	survivor := seedAliasContainer(t, db, "vm2")

	if _, err := db.CreateContainerAlias(doomed.ID, "app.example.org"); err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}
	if _, err := db.CreateContainerAlias(survivor.ID, "keep.example.org"); err != nil {
		t.Fatalf("CreateContainerAlias: %v", err)
	}

	if err := db.DeleteUser(doomed.OwnerID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM container_aliases WHERE container_id = ?`, doomed.ID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("deleting the owner left %d alias rows behind", orphans)
	}
	// The released hostname must be claimable again, and it must stop
	// answering the routing lookup that drives certificate issuance.
	if _, err := db.GetVerifiedAliasByHostname("app.example.org"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a deleted user's hostname still resolves: %v", err)
	}
	if _, err := db.CreateContainerAlias(survivor.ID, "app.example.org"); err != nil {
		t.Errorf("re-claim a deleted user's hostname: %v", err)
	}
	// Another user's aliases must be untouched.
	kept, err := db.ListAliasesByContainer(survivor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Fatalf("want the other user to keep their aliases, got %d", len(kept))
	}
}

func TestAttachAliases(t *testing.T) {
	db := openTestDB(t)
	withAliases := seedAliasContainer(t, db, "vm1")
	without := seedAliasContainer(t, db, "vm2")

	for _, host := range []string{"a.example.org", "b.example.org"} {
		if _, err := db.CreateContainerAlias(withAliases.ID, host); err != nil {
			t.Fatalf("CreateContainerAlias(%s): %v", host, err)
		}
	}

	if err := db.AttachAliases(withAliases, without); err != nil {
		t.Fatalf("AttachAliases: %v", err)
	}
	if len(withAliases.Aliases) != 2 {
		t.Fatalf("want 2 aliases attached, got %d", len(withAliases.Aliases))
	}
	if withAliases.Aliases[0].Hostname != "a.example.org" {
		t.Errorf("aliases are not in a stable order: got %q first", withAliases.Aliases[0].Hostname)
	}
	if without.Aliases != nil {
		t.Errorf("a VM with no aliases must be left nil, got %v", without.Aliases)
	}

	// Re-attaching must replace, not append: a card rendered twice in one
	// session would otherwise show every hostname twice.
	if err := db.AttachAliases(withAliases); err != nil {
		t.Fatalf("AttachAliases again: %v", err)
	}
	if len(withAliases.Aliases) != 2 {
		t.Fatalf("want 2 aliases after re-attach, got %d", len(withAliases.Aliases))
	}
}

func TestAttachAliasesTolerantOfEmptyInput(t *testing.T) {
	db := openTestDB(t)
	if err := db.AttachAliases(); err != nil {
		t.Fatalf("no containers: %v", err)
	}
	if err := db.AttachAliases(nil); err != nil {
		t.Fatalf("nil container: %v", err)
	}
}
