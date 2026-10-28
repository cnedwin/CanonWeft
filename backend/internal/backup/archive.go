// Package backup implements bounded, data-only snapshots. Archives are untrusted.
package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"novelstudio/internal/storage"
	"novelstudio/migrations"
	"regexp"
	"slices"
	"strings"
	"time"
)

const maxArchiveBytes int64 = 256 << 20
const maxDataBytes int64 = 128 << 20
const maxEntries = 10000

// A business migration bump must explicitly update and qualify the backup
// catalog/reference graph. Never inherit support merely from SchemaVersion.
const supportedSchemaVersion = 35
const legacySchemaVersion = 24

var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var sourceName = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)
var uuidPattern = regexp.MustCompile(`^[a-f0-9-]{36}$`)

type Source struct {
	ServerUUID string `json:"server_uuid"`
	Database   string `json:"database"`
}
type Migration struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Origin  string `json:"origin"`
}
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type Manifest struct {
	Format     int         `json:"format"`
	Schema     int         `json:"schema"`
	Source     Source      `json:"source"`
	SnapshotAt time.Time   `json:"snapshot_at"`
	Migrations []Migration `json:"migrations"`
	Files      []File      `json:"files"`
	Exclusions []string    `json:"exclusions"`
	Authority  *Provenance `json:"authority,omitempty"`
}
type Provenance struct {
	LedgerID string `json:"ledger_id"`
	Protocol int    `json:"protocol"`
}

// Byte cells preserve binary vectors, exact JSON text and SQL NULL independently.
type Row [][]byte
type Data map[string][]Row
type Archive struct {
	Manifest Manifest
	Data     Data
	Blobs    map[string][]byte
}
type tableSpec struct{ name, columns string }

func (t tableSpec) cols() []string        { return strings.Split(t.columns, ",") }
func (t tableSpec) index(name string) int { return slices.Index(t.cols(), name) }
func (t tableSpec) value(row Row, name string) string {
	i := t.index(name)
	if i < 0 {
		return ""
	}
	return string(row[i])
}
func (t tableSpec) set(row Row, name, value string) {
	if i := t.index(name); i >= 0 {
		row[i] = []byte(value)
	}
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func migrationCatalog() ([]Migration, error) {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var result []Migration
	for _, entry := range entries {
		if entry.Name() == migrations.NovelEntryName || entry.Name() == migrations.NovelSettingsName {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, Migration{Version: len(result) + 1, Name: entry.Name(), SHA256: digest([]byte(strings.ReplaceAll(string(raw), "\r\n", "\n")))})
	}
	if len(result) != supportedSchemaVersion || (storage.SchemaVersion != supportedSchemaVersion && !hasRetentionSchema(storage.SchemaVersion)) {
		return nil, errors.New("unsupported migration catalog")
	}
	return result, nil
}
func validateManifest(m Manifest) error {
	if m.Format != 1 || (!hasRetentionSchema(m.Schema) && m.Schema != supportedSchemaVersion && m.Schema != 34 && m.Schema != 33 && m.Schema != 32 && m.Schema != 31 && m.Schema != 30 && m.Schema != 29 && m.Schema != 28 && m.Schema != 27 && m.Schema != 26 && m.Schema != 25 && m.Schema != legacySchemaVersion) {
		return errors.New("unsupported archive/schema version")
	}
	if !uuidPattern.MatchString(m.Source.ServerUUID) || !sourceName.MatchString(m.Source.Database) || m.SnapshotAt.IsZero() {
		return errors.New("invalid snapshot identity")
	}
	if m.Schema >= 25 && m.Authority == nil {
		return errors.New("protected ledger provenance missing")
	}
	if m.Authority != nil && (!hex32.MatchString(m.Authority.LedgerID) || m.Authority.Protocol != 1) {
		return errors.New("invalid ledger provenance")
	}
	if err := validateMigrationHistory(m.Schema, m.Migrations); err != nil {
		return err
	}
	if !slices.Equal(m.Exclusions, exclusions) && !(hasRetentionSchema(m.Schema) && slices.Equal(m.Exclusions, retentionExclusions)) {
		return errors.New("exclusion contract mismatch")
	}
	return nil
}

// Both live reader provisioning and archives must establish the same compiled
// migration provenance before selecting a table or privilege catalog.
func validateMigrationHistory(schema int, history []Migration) error {
	expected, err := migrationCatalog()
	if err != nil {
		return err
	}
	if hasRetentionSchema(schema) {
		pending, _, err := retentionMigration()
		if err != nil {
			return err
		}
		expected = append(expected, pending)
		if schema >= 37 {
			next, err := novelMigration()
			if err != nil {
				return err
			}
			expected = append(expected, next)
			if schema == 38 {
				settings, e := novelSettingsMigration()
				if e != nil {
					return e
				}
				expected = append(expected, settings)
			}
		}
	} else {
		if schema < legacySchemaVersion || schema > len(expected) {
			return errors.New("unsupported migration history")
		}
		expected = expected[:schema]
	}
	if len(history) != len(expected) {
		return errors.New("incomplete migration history")
	}
	for i, want := range expected {
		got := history[i]
		if got.Version != want.Version || got.Name != want.Name || got.SHA256 != want.SHA256 || (got.Origin != "EXECUTED" && got.Origin != "LEGACY_BASELINE") {
			return errors.New("migration checksum/provenance mismatch")
		}
		if hasRetentionSchema(got.Version) && got.Origin != "EXECUTED" {
			return errors.New("retention migration must have executed provenance")
		}
	}
	return nil
}
func ValidateArchive(r io.ReaderAt, size int64) (*Archive, error) {
	return validateArchiveContents(r, size)
}

func validateArchiveContents(r io.ReaderAt, size int64) (*Archive, error) {
	if size <= 0 || size > maxArchiveBytes {
		return nil, errors.New("archive size limit")
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	if len(zr.File) > maxEntries {
		return nil, errors.New("archive entry limit")
	}
	files := map[string][]byte{}
	var total int64
	for _, f := range zr.File {
		limit := storage.MaxBlobBytes
		switch f.Name {
		case "manifest.json":
			limit = 1 << 20
		case "data.json":
			limit = maxDataBytes
		default:
			parts := strings.Split(f.Name, "/")
			if len(parts) != 3 || parts[0] != "blobs" || !hex32.MatchString(parts[1]) || !hex64.MatchString(parts[2]) {
				return nil, errors.New("unrecognized archive entry")
			}
		}
		if _, exists := files[f.Name]; exists || !f.Mode().IsRegular() || f.Mode().Perm()&0111 != 0 {
			return nil, errors.New("duplicate or unsafe entry")
		}
		if f.UncompressedSize64 > uint64(limit) {
			return nil, errors.New("entry expansion limit")
		}
		total += int64(f.UncompressedSize64)
		if total > maxArchiveBytes {
			return nil, errors.New("archive expansion limit")
		}
		reader, err := f.Open()
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(reader, limit+1))
		reader.Close()
		if readErr != nil || int64(len(raw)) > limit || uint64(len(raw)) != f.UncompressedSize64 {
			return nil, errors.New("corrupt entry")
		}
		files[f.Name] = raw
	}
	a := &Archive{Blobs: map[string][]byte{}}
	if err := strictJSON(files["manifest.json"], &a.Manifest); err != nil {
		return nil, err
	}
	if err := validateManifest(a.Manifest); err != nil {
		return nil, err
	}
	if len(a.Manifest.Files) != len(files)-1 {
		return nil, errors.New("manifest inventory mismatch")
	}
	seen := map[string]bool{}
	for _, f := range a.Manifest.Files {
		raw, ok := files[f.Path]
		if !ok || f.Path == "manifest.json" || seen[f.Path] || f.Bytes != int64(len(raw)) || f.SHA256 != digest(raw) {
			return nil, errors.New("file hash/inventory mismatch")
		}
		seen[f.Path] = true
		if strings.HasPrefix(f.Path, "blobs/") {
			if strings.Split(f.Path, "/")[2] != f.SHA256 {
				return nil, errors.New("blob identity mismatch")
			}
			a.Blobs[f.Path] = raw
		}
	}
	if !seen["data.json"] {
		return nil, errors.New("missing data")
	}
	if err := strictJSON(files["data.json"], &a.Data); err != nil {
		return nil, err
	}
	// Explicit trusted schema24 ->25 data conversion. No archive SQL executes;
	// the target is migrated by our checked-in migration catalog before insert.
	if a.Manifest.Schema == legacySchemaVersion {
		for i, row := range a.Data["projects"] {
			if len(row) != 5 {
				return nil, errors.New("legacy project column mismatch")
			}
			a.Data["projects"][i] = append(row, nil)
		}
	}
	if a.Manifest.Schema < 26 {
		if _, exists := a.Data["generation_previews"]; exists {
			return nil, errors.New("unexpected preview table in legacy archive")
		}
		a.Data["generation_previews"] = nil
	}
	if a.Manifest.Schema < 27 {
		for _, name := range []string{"working_indexes", "working_chunks"} {
			if _, exists := a.Data[name]; exists {
				return nil, errors.New("unexpected working index table in legacy archive")
			}
			a.Data[name] = nil
		}
		for i, row := range a.Data["index_jobs"] {
			if len(row) != 13 {
				return nil, errors.New("legacy index job column mismatch")
			}
			a.Data["index_jobs"][i] = append(row, []byte("published"))
		}
	}
	if a.Manifest.Schema < 28 {
		for _, name := range []string{"qualification_runs", "qualification_steps", "capability_qualifications", "capability_bindings", "capability_binding_heads", "qualification_approvals", "qualification_schedules"} {
			if _, exists := a.Data[name]; exists {
				return nil, errors.New("unexpected qualification table in legacy archive")
			}
			a.Data[name] = nil
		}
	}
	if a.Manifest.Schema < 29 {
		for _, name := range []string{"qualification_week_grants", "qualification_calls"} {
			if _, exists := a.Data[name]; exists {
				return nil, errors.New("unexpected qualification budget table in legacy archive")
			}
			a.Data[name] = nil
		}
	}
	if err := upgradeQualificationAccounting(a); err != nil {
		return nil, err
	}
	if err := upgradeRecipes(a); err != nil {
		return nil, err
	}
	if err := upgradeMemory(a); err != nil {
		return nil, err
	}
	if err := upgradeIngestJobs(a); err != nil {
		return nil, err
	}
	if err := upgradePatchDrafts(a); err != nil {
		return nil, err
	}
	if err := upgradeSemanticReviews(a); err != nil {
		return nil, err
	}
	business, metadata, err := splitRetentionData(a.Manifest.Schema, a.Data)
	if err != nil {
		return nil, err
	}
	if len(metadata) != 0 {
		refs, err := validatedRetentionReferences(a, business, metadata)
		if err != nil {
			return nil, err
		}
		if err := validateBlobReferenceSet(refs, a.Blobs); err != nil {
			return nil, err
		}
		if err := validateMemoryObjects(business, a.Blobs, true); err != nil {
			return nil, err
		}
		return a, nil
	}
	if err := validateData(business); err != nil {
		return nil, err
	}
	refs, err := blobReferences(business)
	if err != nil {
		return nil, err
	}
	if len(refs) != len(a.Blobs) {
		return nil, errors.New("unreferenced or missing blob")
	}
	for path := range refs {
		if _, ok := a.Blobs[path]; !ok {
			return nil, fmt.Errorf("missing object %s", path)
		}
	}
	if err := validateMemoryObjects(a.Data, a.Blobs); err != nil {
		return nil, err
	}
	return a, nil
}
func writeArchive(w io.Writer, a *Archive) error {
	data, err := json.Marshal(a.Data)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxDataBytes {
		return errors.New("data size limit")
	}
	files := map[string][]byte{"data.json": data}
	for p, b := range a.Blobs {
		files[p] = b
	}
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	slices.Sort(names)
	a.Manifest.Files = nil
	var total int64
	for _, p := range names {
		b := files[p]
		total += int64(len(b))
		a.Manifest.Files = append(a.Manifest.Files, File{p, digest(b), int64(len(b))})
	}
	if total > maxArchiveBytes || len(names)+1 > maxEntries {
		return errors.New("archive capacity limit")
	}
	manifest, err := json.Marshal(a.Manifest)
	if err != nil {
		return err
	}
	if len(manifest) > 1<<20 {
		return errors.New("manifest limit")
	}
	files["manifest.json"] = manifest
	names = append(names, "manifest.json")
	z := zip.NewWriter(w)
	for _, p := range names {
		h := &zip.FileHeader{Name: p, Method: zip.Deflate}
		h.SetMode(0600)
		entry, err := z.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err = entry.Write(files[p]); err != nil {
			return err
		}
	}
	return z.Close()
}
