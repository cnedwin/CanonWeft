package service

import (
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func TestCleanupPreparesEveryDirectExecutionObject(t *testing.T) {
	project, id, object := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 64)
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, rule := range retention.Rules() {
		if !retention.DirectExecutionObjectRule(rule.Key) && !retention.DirectImportObjectRule(rule.Key) {
			continue
		}
		t.Run(rule.Key, func(t *testing.T) {
			parts := strings.Split(rule.Key, ".")
			spec, _ := retention.OwnerSpec(parts[0])
			values := map[string]string{}
			for _, column := range spec.Columns {
				values[column.Column] = ""
			}
			values["id"], values["project_id"], values[parts[1]] = id, project, object
			index, err := retention.NewSourceIndex([]retention.SourceRow{{Table: parts[0], Values: values}})
			if err != nil {
				t.Fatal(err)
			}
			key, _ := retention.RowOwnerKey(parts[0], map[string]string{"id": id})
			ref, _ := retention.ReferenceID(project, parts[0], key, parts[1])
			item := retention.Expiry{ReferenceID: ref, Revision: 1, Rule: rule.Key, PayloadHash: digest(object), Deadline: at, Storage: retention.Blob}
			batch := retention.ExpiryOwnerBatch{Index: index, Source: retention.SourceKey{Table: parts[0], Primary: map[string]string{"id": id}}, Items: []retention.Expiry{item}}
			proofs, err := prepareCleanupBatch(batch, project, at)
			if err != nil || len(proofs) != 1 || proofs[0].Summary.ValueHash != object || proofs[0].Materialization != retention.BlobReference {
				t.Fatal("execution object proof not bound to actual hash", err)
			}
			batch.Items[0].PayloadHash = strings.Repeat("d", 64)
			if _, err := prepareCleanupBatch(batch, project, at); err == nil {
				t.Fatal("stale object reference accepted")
			}
		})
	}
}
