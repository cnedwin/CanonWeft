package backup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/storage"
)

func (r *LogReplay) checkLogDataCapacity(data Data) error {
	serialized, err := logSchemaData(data, r.baseline.Schema)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(serialized)
	if err != nil || int64(len(raw)) > r.capacity.data {
		return errors.New("recovery state exceeds data capacity")
	}
	return nil
}

// Materialized recovery archives use ZIP STORE. This makes a conservative
// complete size bound independent of compression ratios, without repeatedly
// compressing the whole novel for every candidate transaction boundary.
func (r *LogReplay) materializationFiles(data Data, objects map[string][]byte, at time.Time) (map[string][]byte, error) {
	serialized, err := logSchemaData(data, r.baseline.Schema)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(serialized)
	if err != nil || int64(len(raw)) > r.capacity.data {
		return nil, errors.New("recovery state exceeds data capacity")
	}
	refs, err := r.stateReferences(data, at)
	if err != nil {
		return nil, err
	}
	if len(refs)+2 > r.capacity.entries {
		return nil, errors.New("recovery state exceeds archive entry capacity")
	}
	files := map[string][]byte{"data.json": raw}
	for path := range refs {
		body, exists := objects[path]
		parts := strings.Split(path, "/")
		if !exists || int64(len(body)) > storage.MaxBlobBytes || digest(body) != parts[2] {
			return nil, errors.New("selected recovery object missing or corrupt")
		}
		files[path] = body
	}
	manifest := r.archive.Manifest
	if err := validateMemoryObjects(data, objects, hasRetentionSchema(r.baseline.Schema)); err != nil {
		return nil, err
	}
	manifest.SnapshotAt = at
	manifest.Files = nil
	var names []string
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		manifest.Files = append(manifest.Files, File{Path: name, SHA256: digest(files[name]), Bytes: int64(len(files[name]))})
	}
	metadata, err := json.Marshal(manifest)
	if err != nil || int64(len(metadata)) > r.capacity.manifest {
		return nil, errors.New("recovery state exceeds manifest capacity")
	}
	files["manifest.json"] = metadata
	// STORE, ASCII names, no extras/comments, <4GiB entries and <65535 files:
	// 128 bytes per entry and footer conservatively cover local/central headers
	// and descriptors. Check the actual emitted size again when writing.
	size := int64(128)
	for name, body := range files {
		size += int64(len(body)) + 128 + 2*int64(len(name))
		if size > r.capacity.archive {
			return nil, errors.New("recovery state exceeds complete archive capacity")
		}
	}
	return files, nil
}

func (r *LogReplay) writeMaterializedArchive(data Data, objects map[string][]byte, at time.Time) ([]byte, error) {
	files, err := r.materializationFiles(data, objects, at)
	if err != nil {
		return nil, err
	}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if int64(output.Len()) > r.capacity.archive {
		return nil, errors.New("materialized archive capacity exceeded")
	}
	raw := output.Bytes()
	if _, err := validateArchiveContents(bytes.NewReader(raw), int64(len(raw))); err != nil {
		return nil, err
	}
	return raw, nil
}
