package db

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxAliasesPerContainer bounds how many custom hostnames one VM can claim.
// Every alias is a name the platform will ask a certificate authority about,
// so an unbounded list is an unbounded ACME bill.
const MaxAliasesPerContainer = 10

// ErrAliasHostnameTaken means another VM has verified the hostname. Callers
// turn it into a 409 rather than a generic failure, because the owner can fix
// it by picking a different name.
var ErrAliasHostnameTaken = errors.New("hostname is already in use by another VM")

// ErrAliasAlreadyOnVM means this VM already lists the hostname. It is separate
// from ErrAliasHostnameTaken so the owner is not told a domain they already own
// belongs to somebody else.
var ErrAliasAlreadyOnVM = errors.New("this VM already has that custom domain")

// ContainerAlias is a custom hostname pointed at a VM's workload.
type ContainerAlias struct {
	ID          string
	ContainerID string
	Hostname    string
	// Verified records whether the hostname was observed resolving to this
	// gateway. Routing and certificate issuance both refuse an unverified
	// alias: without the check anyone could claim a name they do not control.
	Verified bool
	// VerifiedAt is the time of the last successful check, nil while the alias
	// has never verified.
	VerifiedAt *time.Time
	// LastError explains the most recent failed check, so the owner can see
	// what their DNS actually returned instead of a bare "not verified".
	LastError string
	CreatedAt time.Time
}

// aliasLabelRE matches a single DNS label.
var aliasLabelRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// aliasColumns keeps every read of an alias in sync.
const aliasColumns = `id, container_id, hostname, verified, verified_at, last_error, created_at`

func scanAlias(row interface{ Scan(...any) error }) (*ContainerAlias, error) {
	a := &ContainerAlias{}
	var verifiedAt sql.NullTime
	if err := row.Scan(&a.ID, &a.ContainerID, &a.Hostname, &a.Verified, &verifiedAt, &a.LastError, &a.CreatedAt); err != nil {
		return nil, err
	}
	if verifiedAt.Valid {
		t := verifiedAt.Time
		a.VerifiedAt = &t
	}
	return a, nil
}

// ValidAliasHostname normalises a user-supplied hostname and reports why it
// cannot be used, if it cannot. domain is the gateway's own base domain; names
// under it are rejected because subdomain routing already owns them and an
// alias there would silently shadow a VM host.
func ValidAliasHostname(hostname, domain string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(hostname))
	// A trailing dot is a valid way to write an FQDN, but everything
	// downstream compares against Host headers, which never carry one.
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", errors.New("hostname is empty")
	}
	if len(h) > 253 {
		return "", errors.New("hostname is longer than 253 characters")
	}
	if !strings.Contains(h, ".") {
		return "", errors.New("hostname must be a full domain name, for example app.example.org")
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" {
			return "", errors.New("hostname has an empty label")
		}
		if len(label) > 63 {
			return "", fmt.Errorf("label %q is longer than 63 characters", label)
		}
		if !aliasLabelRE.MatchString(label) {
			return "", fmt.Errorf("label %q may only contain a-z, 0-9 and hyphens, and cannot start or end with a hyphen", label)
		}
	}
	// A purely numeric last label would be an IP address, not a name a
	// certificate authority will issue for.
	last := h[strings.LastIndex(h, ".")+1:]
	if strings.Trim(last, "0123456789") == "" {
		return "", errors.New("hostname must not end in a numeric label")
	}
	if d := strings.ToLower(strings.TrimSpace(domain)); d != "" {
		if h == d || strings.HasSuffix(h, "."+d) {
			return "", fmt.Errorf("%s is already routed by this gateway — pick a hostname outside %s", h, d)
		}
	}
	return h, nil
}

// CreateContainerAlias claims a hostname for a container. The alias starts
// unverified: nothing is routed and no certificate is issued until a DNS check
// confirms the owner actually controls the name.
//
// A name another VM has already VERIFIED is refused. A name merely pending on
// another VM is not: an unverified claim is a request, not a reservation, and
// treating it as one would let anybody park a domain they do not control.
func (db *DB) CreateContainerAlias(containerID, hostname string) (*ContainerAlias, error) {
	count, err := db.countAliases(containerID)
	if err != nil {
		return nil, err
	}
	if count >= MaxAliasesPerContainer {
		return nil, fmt.Errorf("a VM can have at most %d custom domains", MaxAliasesPerContainer)
	}
	if owner, err := db.GetVerifiedAliasByHostname(hostname); err == nil && owner.ContainerID != containerID {
		return nil, ErrAliasHostnameTaken
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Checked separately so the owner is told they already have this domain
	// rather than that somebody else does.
	var mine int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM container_aliases WHERE container_id = ? AND hostname = ?`,
		containerID, hostname,
	).Scan(&mine); err != nil {
		return nil, fmt.Errorf("check existing container alias: %w", err)
	}
	if mine > 0 {
		return nil, ErrAliasAlreadyOnVM
	}

	a := &ContainerAlias{ID: uuid.NewString(), ContainerID: containerID, Hostname: hostname}
	_, err = db.Exec(
		`INSERT INTO container_aliases (id, container_id, hostname) VALUES (?, ?, ?)`,
		a.ID, a.ContainerID, a.Hostname,
	)
	if err != nil {
		// A concurrent request beat us between the checks above and this
		// insert. An ordinary outcome the caller reports as a conflict.
		if isUniqueViolation(err) {
			return nil, ErrAliasHostnameTaken
		}
		return nil, fmt.Errorf("create container alias: %w", err)
	}
	return db.GetAliasByID(a.ID)
}

// isUniqueViolation reports whether err came from a unique index rejecting a
// write, which is a normal outcome here rather than a failure.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

func (db *DB) countAliases(containerID string) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM container_aliases WHERE container_id = ?`, containerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count container aliases: %w", err)
	}
	return n, nil
}

// GetAliasByID returns an alias by primary key.
func (db *DB) GetAliasByID(id string) (*ContainerAlias, error) {
	a, err := scanAlias(db.QueryRow(`SELECT `+aliasColumns+` FROM container_aliases WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get container alias: %w", err)
	}
	return a, nil
}

// GetVerifiedAliasByHostname resolves a Host header to an alias. Unverified
// rows are invisible here on purpose: this is the lookup both request routing
// and certificate issuance go through.
func (db *DB) GetVerifiedAliasByHostname(hostname string) (*ContainerAlias, error) {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	a, err := scanAlias(db.QueryRow(`SELECT `+aliasColumns+` FROM container_aliases WHERE hostname = ? AND verified = 1`, h))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get verified container alias: %w", err)
	}
	return a, nil
}

// ListAliasesByContainer returns a container's aliases, oldest first so the
// dashboard order does not shuffle when one is re-verified.
func (db *DB) ListAliasesByContainer(containerID string) ([]*ContainerAlias, error) {
	rows, err := db.Query(`SELECT `+aliasColumns+` FROM container_aliases WHERE container_id = ? ORDER BY created_at, hostname`, containerID)
	if err != nil {
		return nil, fmt.Errorf("list container aliases: %w", err)
	}
	defer rows.Close()

	var aliases []*ContainerAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, fmt.Errorf("scan container alias: %w", err)
		}
		aliases = append(aliases, a)
	}
	return aliases, rows.Err()
}

// SetAliasVerification records the outcome of a DNS check.
//
// Passing verified false clears the flag, which takes the name out of routing
// immediately. Reverify is what normally does that; an owner pressing Re-check
// can too.
//
// Verifying does two things in one transaction. It sets the flag, and it
// deletes every pending claim on the same hostname held by another VM. Those
// claims would otherwise sit there indefinitely as an option on the name: the
// DNS check proves a hostname points at THIS GATEWAY, not who owns it, so on a
// shared gateway anyone can satisfy it. If the rightful owner's flag were ever
// cleared — a transient re-check failure is enough, and a gateway that cannot
// resolve its own DOMAIN fails every alias at once — a parked claim could then
// verify and take the name, and the traffic, under a valid certificate.
// Destroying competing claims at the moment someone proves the name points here
// shrinks that window from indefinite to the genuine race at first verification,
// which the partial unique index arbitrates.
//
// Marking an alias verified can fail with ErrAliasHostnameTaken when another VM
// won that race: only one verified row per hostname is admitted.
func (db *DB) SetAliasVerification(id string, verified bool, reason string) error {
	if !verified {
		_, err := db.Exec(
			`UPDATE container_aliases SET verified = 0, verified_at = NULL, last_error = ? WHERE id = ?`,
			reason, id,
		)
		if err != nil {
			return fmt.Errorf("update container alias verification: %w", err)
		}
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("update container alias verification: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`UPDATE container_aliases SET verified = 1, verified_at = ?, last_error = '' WHERE id = ?`,
		time.Now().UTC(), id,
	); err != nil {
		if isUniqueViolation(err) {
			return ErrAliasHostnameTaken
		}
		return fmt.Errorf("update container alias verification: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM container_aliases
		 WHERE hostname = (SELECT hostname FROM container_aliases WHERE id = ?)
		   AND container_id != (SELECT container_id FROM container_aliases WHERE id = ?)
		   AND verified = 0`,
		id, id,
	); err != nil {
		return fmt.Errorf("drop competing claims: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return ErrAliasHostnameTaken
		}
		return fmt.Errorf("update container alias verification: %w", err)
	}
	return nil
}

// AliasWithOwner is an alias together with the VM and account behind it, which
// is what an operator needs to judge a hostname dispute: the row alone says
// nothing about who is holding the name.
type AliasWithOwner struct {
	ContainerAlias
	ContainerName string
	OwnerID       string
	OwnerEmail    string
}

// ListAllAliases returns every alias on the platform with its VM and owner,
// verified ones first so a disputed hostname's current holder is easy to spot.
func (db *DB) ListAllAliases() ([]*AliasWithOwner, error) {
	rows, err := db.Query(`
		SELECT a.id, a.container_id, a.hostname, a.verified, a.verified_at, a.last_error, a.created_at,
		       c.name, u.id, u.email
		FROM container_aliases a
		JOIN containers c ON c.id = a.container_id
		JOIN users u ON u.id = c.owner_id
		ORDER BY a.hostname, a.verified DESC, a.created_at`)
	if err != nil {
		return nil, fmt.Errorf("list all aliases: %w", err)
	}
	defer rows.Close()

	var out []*AliasWithOwner
	for rows.Next() {
		a := &AliasWithOwner{}
		var verifiedAt sql.NullTime
		if err := rows.Scan(&a.ID, &a.ContainerID, &a.Hostname, &a.Verified, &verifiedAt,
			&a.LastError, &a.CreatedAt, &a.ContainerName, &a.OwnerID, &a.OwnerEmail); err != nil {
			return nil, fmt.Errorf("scan alias with owner: %w", err)
		}
		if verifiedAt.Valid {
			t := verifiedAt.Time
			a.VerifiedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAliasesByHostname returns every claim on a hostname, across all VMs.
func (db *DB) ListAliasesByHostname(hostname string) ([]*ContainerAlias, error) {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	rows, err := db.Query(`SELECT `+aliasColumns+` FROM container_aliases WHERE hostname = ? ORDER BY verified DESC, created_at`, h)
	if err != nil {
		return nil, fmt.Errorf("list aliases by hostname: %w", err)
	}
	defer rows.Close()

	var aliases []*ContainerAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, fmt.Errorf("scan container alias: %w", err)
		}
		aliases = append(aliases, a)
	}
	return aliases, rows.Err()
}

// DeleteAliasesByHostname removes every claim on a hostname and reports how
// many went. It frees the name platform-wide rather than for one VM, because
// the operator using it is settling ownership of the name itself.
func (db *DB) DeleteAliasesByHostname(hostname string) (int64, error) {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	res, err := db.Exec(`DELETE FROM container_aliases WHERE hostname = ?`, h)
	if err != nil {
		return 0, fmt.Errorf("release hostname: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("release hostname: %w", err)
	}
	return n, nil
}

// ListVerifiedAliases returns every alias currently routed, oldest check first
// so a periodic re-check works through the stalest ones before the fresh ones.
func (db *DB) ListVerifiedAliases() ([]*ContainerAlias, error) {
	rows, err := db.Query(`SELECT ` + aliasColumns + ` FROM container_aliases WHERE verified = 1 ORDER BY verified_at`)
	if err != nil {
		return nil, fmt.Errorf("list verified aliases: %w", err)
	}
	defer rows.Close()

	var aliases []*ContainerAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, fmt.Errorf("scan container alias: %w", err)
		}
		aliases = append(aliases, a)
	}
	return aliases, rows.Err()
}

// DeleteContainerAlias removes an alias by ID.
func (db *DB) DeleteContainerAlias(id string) error {
	if _, err := db.Exec(`DELETE FROM container_aliases WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete container alias: %w", err)
	}
	return nil
}

// AttachAliases fills in the Aliases field of the given containers with one
// query, so a list page does not issue a lookup per card.
func (db *DB) AttachAliases(containers ...*Container) error {
	byID := make(map[string]*Container, len(containers))
	args := make([]any, 0, len(containers))
	placeholders := make([]string, 0, len(containers))
	for _, c := range containers {
		if c == nil {
			continue
		}
		c.Aliases = nil
		byID[c.ID] = c
		args = append(args, c.ID)
		placeholders = append(placeholders, "?")
	}
	if len(args) == 0 {
		return nil
	}

	rows, err := db.Query(
		`SELECT `+aliasColumns+` FROM container_aliases WHERE container_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at, hostname`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("attach container aliases: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return fmt.Errorf("scan container alias: %w", err)
		}
		if c := byID[a.ContainerID]; c != nil {
			c.Aliases = append(c.Aliases, a)
		}
	}
	return rows.Err()
}
