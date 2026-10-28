// Package retention plans bounded content expiry. Plans are not authorization
// to delete: execution must recheck the inventory revision under business locks.
package retention

import "time"

type Class string

const (
	Content            Class = "CONTENT"
	Candidate          Class = "CANDIDATE"
	RawRun             Class = "RAW_RUN"
	FailedMedia        Class = "FAILED_MEDIA"
	Debug              Class = "DEBUG"
	Temporary          Class = "TEMPORARY"
	Audit              Class = "AUDIT"
	ProtectedAuthority Class = "PROTECTED_AUTHORITY"
	Backup             Class = "BACKUP"
)

type PayloadState string

const (
	Retained PayloadState = "RETAINED"
	Expired  PayloadState = "EXPIRED"
)

type StorageKind string

const (
	Blob     StorageKind = "BLOB"
	Inline   StorageKind = "INLINE"
	Metadata StorageKind = "METADATA"
)

type Lifecycle struct {
	TimeBasis         TimeBasis `json:"time_basis"`
	SourceDeletedAt   time.Time `json:"source_deleted_at"`
	LedgerID          string    `json:"ledger_id"`
	DeleteSequence    uint64    `json:"delete_sequence"`
	DeletedAt         time.Time `json:"deleted_at"`
	RecoverUntil      time.Time `json:"recover_until"`
	CleanupEligibleAt time.Time `json:"cleanup_eligible_at"`
	OnlineDeadline    time.Time `json:"online_deadline"`
	BackupDeadline    time.Time `json:"backup_deadline"`
}
type ReferenceRule struct {
	Key        string
	Storage    StorageKind
	Class      Class
	Promotable bool
}
type Reference struct {
	Sources         []ReferenceSource `json:"sources,omitempty"`
	TimeBasis       TimeBasis         `json:"time_basis"`
	FirstVerifiedAt time.Time         `json:"first_verified_at"`
	ID              string            `json:"id"`
	Rule            string            `json:"rule"`
	ObjectHash      string            `json:"object_hash"`
	PayloadHash     string            `json:"payload_hash"`
	CreatedAt       time.Time         `json:"created_at"`
	Revision        int64             `json:"revision"`
	State           PayloadState      `json:"state"`
	ExpiredAt       time.Time         `json:"expired_at"`
	ContentRoot     bool              `json:"content_root"`
	LeaseUntil      time.Time         `json:"lease_until"`
	AccountingOpen  bool              `json:"accounting_open"`
}

// ReferenceSource binds a copied material to the exact registered source.
// Only a trusted source collector may construct these pins. A pin is not an
// authorization or a claim that the containing inventory is complete.
type ReferenceSource struct {
	ReferenceID string `json:"reference_id"`
	Revision    int64  `json:"revision"`
	PayloadHash string `json:"payload_hash"`
}
type TimeBasis string

const (
	SourceUTC        TimeBasis = "SOURCE_UTC"
	LegacyRegistered TimeBasis = "LEGACY_REGISTERED"
)

type ObjectState string

const (
	ObjectPresent ObjectState = "PRESENT"
	ObjectDeleted ObjectState = "DELETED"
)

type Object struct {
	FirstSeenAt time.Time   `json:"first_seen_at"`
	LeaseUntil  time.Time   `json:"lease_until"`
	Hash        string      `json:"hash"`
	State       ObjectState `json:"state"`
	Revision    int64       `json:"revision"`
}
type Inventory struct {
	ProjectID            string      `json:"project_id"`
	Revision             int64       `json:"revision"`
	CatalogVersion       int         `json:"catalog_version"`
	CoveredTables        []string    `json:"covered_tables"`
	CapturedAt           time.Time   `json:"captured_at"`
	Complete             bool        `json:"complete"`
	Lifecycle            Lifecycle   `json:"lifecycle"`
	References           []Reference `json:"references"`
	HistoricalReferences []Reference `json:"historical_references,omitempty"`
	Objects              []Object    `json:"objects"`
}
type Expiry struct {
	ReferenceID string      `json:"reference_id"`
	Rule        string      `json:"rule"`
	PayloadHash string      `json:"payload_hash"`
	Revision    int64       `json:"revision"`
	Deadline    time.Time   `json:"deadline"`
	Storage     StorageKind `json:"storage"`
}
type Plan struct {
	ProjectID         string    `json:"project_id"`
	InventoryRevision int64     `json:"inventory_revision"`
	InventoryHash     string    `json:"inventory_hash"`
	Cutoff            time.Time `json:"cutoff"`
	Expire            []Expiry  `json:"expire"`
	DeleteObjects     []string  `json:"delete_objects"`
	Blocked           []string  `json:"blocked"`
	Overdue           bool      `json:"overdue"`
	Hash              string    `json:"hash"`
}
