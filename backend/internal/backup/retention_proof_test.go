package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func expiredArchiveFixture(t *testing.T, field string) (expiredArchiveReference, expiredArchiveOwner, []byte) {
	t.Helper()
	project := strings.Repeat("a", 32)
	key, err := retention.OwnerKey(strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	id, err := retention.ReferenceID(project, "generation_tasks", key, field)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	rule, _ := retention.Rule("generation_tasks." + field)
	original := []byte(`{"private_text":"must never survive expiry"}`)
	summary := retention.EvidenceSummary{InputHash: strings.Repeat("c", 64)}
	object := ""
	if rule.Storage == retention.Blob {
		object = summary.InputHash
		original = []byte(object)
		summary.ValueHash = object
	}
	expiry := retention.Expiry{ReferenceID: id, Rule: rule.Key, PayloadHash: digest(original), Revision: 3, Deadline: at, Storage: rule.Storage}
	projection, err := retention.ProjectExpired(expiry, original, summary, at)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	value := proof
	if rule.Storage == retention.Blob {
		value = original
	}
	ref := expiredArchiveReference{ID: id, ProjectID: project, Table: "generation_tasks", OwnerKey: key, Field: field, Storage: rule.Storage, State: retention.Expired, Revision: 3, EvidenceRevision: 3, PayloadHash: expiry.PayloadHash, ObjectHash: object, ProjectionHash: digest(proof), Deadline: at, ExpiredAt: at, EvidenceExpiredAt: at}
	owner := expiredArchiveOwner{ProjectID: project, Table: ref.Table, Key: key, Field: field, Value: value}
	return ref, owner, proof
}

func TestExpiredArchiveProofBindsOriginalIdentityAndExactProjection(t *testing.T) {
	for _, field := range []string{"frozen", "frozen.input_hash"} {
		t.Run(field, func(t *testing.T) {
			ref, owner, proof := expiredArchiveFixture(t, field)
			got, err := validateExpiredArchiveReference(ref, owner, proof)
			if err != nil {
				t.Fatal(err)
			}
			if got.OriginalPayloadHash != ref.PayloadHash || got.ReferenceID != ref.ID {
				t.Fatal("original evidence identity was lost")
			}
			if strings.Contains(string(proof), "must never survive") {
				t.Fatal("projection retained private source text")
			}
		})
	}
}

func TestExpiredReceiptProofCannotMoveToAnotherReceiptOrCall(t *testing.T) {
	project, call := strings.Repeat("a", 32), strings.Repeat("b", 32)
	raw := []byte(`["UNKNOWN",0,"private receipt",""]`)
	receiptID := digest([]byte(call + "\x00" + digest(raw)))
	summary, err := retention.ReceiptEvidence(call, receiptID, raw)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("model_call_receipts", map[string]string{"id": receiptID})
	refID, _ := retention.ReferenceID(project, "model_call_receipts", key, "payload")
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	projection, err := retention.ProjectExpired(retention.Expiry{ReferenceID: refID, Rule: "model_call_receipts.payload", PayloadHash: digest(raw), Revision: 1, Deadline: at, Storage: retention.Inline}, raw, retention.EvidenceSummary{CallID: call, Currency: "USD", State: "UNKNOWN", InputHash: strings.Repeat("d", 64), Receipt: &summary}, at)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.Marshal(projection)
	ref := expiredArchiveReference{ID: refID, ProjectID: project, Table: "model_call_receipts", OwnerKey: key, Field: "payload", Storage: retention.Inline, State: retention.Expired, Revision: 1, EvidenceRevision: 1, PayloadHash: digest(raw), ProjectionHash: digest(proof), Deadline: at, ExpiredAt: at, EvidenceExpiredAt: at}
	owner := expiredArchiveOwner{ProjectID: project, Table: ref.Table, Key: key, Field: "payload", CallID: call, Value: proof}
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err != nil {
		t.Fatal(err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, proof, "", "  "); err != nil {
		t.Fatal(err)
	}
	owner.Value = formatted.Bytes()
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err != nil {
		t.Fatal("MySQL JSON formatting invalidated otherwise identical source evidence", err)
	}
	owner.Value = []byte(strings.Replace(string(proof), `"state":"EXPIRED"`, `"STATE":"EXPIRED"`, 1))
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("case alias was accepted as the stored projection")
	}
	owner.Value = proof
	owner.CallID = strings.Repeat("c", 32)
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("receipt proof transferred between calls")
	}
	owner.CallID = call
	projection.Summary.Receipt.ID = strings.Repeat("e", 64)
	proof, _ = json.Marshal(projection)
	owner.Value = proof
	ref.ProjectionHash = digest(proof)
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("receipt summary identifies a different original receipt")
	}
}

func TestExpiredReconciliationProofPreservesOriginalSourceRevisionAndHash(t *testing.T) {
	project, call, id := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	raw := []byte(`{"id":"` + id + `","call_id":"` + call + `","revision":2,"previous_amount_micros":0,"released_micros":80,"input":{"operation_id":"private-operation","expected_revision":1,"currency":"USD","amount_micros":37,"bill_reference":"private-bill","evidence":"private-evidence","reason":"private-reason","confirmed":true}}`)
	adjustment, err := retention.ReconciliationEvidence(id, call, 2, digest(raw), raw)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("model_reconciliations", map[string]string{"id": id})
	refID, _ := retention.ReferenceID(project, "model_reconciliations", key, "payload")
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	projection, err := retention.ProjectExpired(retention.Expiry{ReferenceID: refID, Rule: "model_reconciliations.payload", PayloadHash: digest(raw), Revision: 1, Deadline: at, Storage: retention.Inline}, raw, retention.EvidenceSummary{CallID: call, Currency: "USD", State: "RECONCILED", InputHash: strings.Repeat("d", 64), Reconciliation: &adjustment}, at)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.Marshal(projection)
	ref := expiredArchiveReference{ID: refID, ProjectID: project, Table: "model_reconciliations", OwnerKey: key, Field: "payload", Storage: retention.Inline, State: retention.Expired, Revision: 1, EvidenceRevision: 1, PayloadHash: digest(raw), ProjectionHash: digest(proof), Deadline: at, ExpiredAt: at, EvidenceExpiredAt: at}
	owner := expiredArchiveOwner{ProjectID: project, Table: ref.Table, Key: key, Field: "payload", CallID: call, ContentHash: digest(raw), SourceRevision: "2", Value: proof}
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"call", "revision", "hash", "key"} {
		bad := owner
		switch kind {
		case "call":
			bad.CallID = strings.Repeat("e", 32)
		case "revision":
			bad.SourceRevision = "3"
		case "hash":
			bad.ContentHash = strings.Repeat("e", 64)
		case "key":
			bad.Key, _ = retention.RowOwnerKey("model_reconciliations", map[string]string{"id": strings.Repeat("e", 32)})
		}
		if _, err := validateExpiredArchiveReference(ref, bad, proof); err == nil {
			t.Fatal("reconciliation evidence detached from source", kind)
		}
	}
}

func TestExpiredArchiveProofRejectsForgedOrTransplantedEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*expiredArchiveReference, *expiredArchiveOwner, *[]byte)
	}{
		{"another-project", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			o.ProjectID = strings.Repeat("d", 32)
		}},
		{"another-row", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			o.Key, _ = retention.OwnerKey(strings.Repeat("d", 32))
		}},
		{"another-field", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) { o.Field = "frozen.input_hash" }},
		{"wrong-original-hash", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			r.PayloadHash = strings.Repeat("d", 64)
		}},
		{"wrong-projection-hash", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			r.ProjectionHash = strings.Repeat("d", 64)
		}},
		{"not-expired", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) { r.State = retention.Retained }},
		{"early-expiry", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			r.Deadline = r.ExpiredAt.Add(time.Second)
		}},
		{"different-expiry", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			r.ExpiredAt = r.ExpiredAt.Add(time.Second)
		}},
		{"zero-revision", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) { r.Revision = 0 }},
		{"different-reference-revision", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) { r.Revision = 4 }},
		{"different-evidence-expiry", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			r.EvidenceExpiredAt = r.EvidenceExpiredAt.Add(time.Second)
		}},
		{"unchanged-private-field", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			o.Value = []byte(`{"private_text":"must never survive expiry"}`)
		}},
		{"extra-private-field", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			*p = append([]byte(`{"prompt":"secret",`), (*p)[1:]...)
			r.ProjectionHash = digest(*p)
			o.Value = *p
		}},
		{"duplicate-state", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			*p = append([]byte(`{"state":"RETAINED",`), (*p)[1:]...)
			r.ProjectionHash = digest(*p)
			o.Value = *p
		}},
		{"case-alias-state", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			*p = append([]byte(`{"STATE":"PRIVATE_CANARY",`), (*p)[1:]...)
			r.ProjectionHash = digest(*p)
			o.Value = *p
		}},
		{"case-alias-summary", func(r *expiredArchiveReference, o *expiredArchiveOwner, p *[]byte) {
			*p = append([]byte(`{"SUMMARY":{"state":"PRIVATE_CANARY"},`), (*p)[1:]...)
			r.ProjectionHash = digest(*p)
			o.Value = *p
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, owner, proof := expiredArchiveFixture(t, "frozen")
			c.change(&ref, &owner, &proof)
			if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
				t.Fatal("invalid expiry evidence accepted")
			}
		})
	}
}

func TestExpiredBlobProofDoesNotTurnAnUnrelatedHashIntoExpiredContent(t *testing.T) {
	ref, owner, proof := expiredArchiveFixture(t, "frozen.input_hash")
	owner.Value = []byte(strings.Repeat("e", 64))
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("different current hash accepted")
	}
	ref, owner, proof = expiredArchiveFixture(t, "frozen.input_hash")
	ref.ObjectHash = strings.Repeat("e", 64)
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("different registered object accepted")
	}
}

func TestExpiredArchiveClearsOnlyAllowedScalarsWithBoundEvidence(t *testing.T) {
	ref, owner, _ := expiredArchiveFixture(t, "frozen")
	ref.Table, ref.Field = "source_imports", "filename"
	owner.Table, owner.Field = ref.Table, ref.Field
	ref.ID, _ = retention.ReferenceID(ref.ProjectID, ref.Table, ref.OwnerKey, ref.Field)
	original := []byte("私密故事构思.docx")
	ref.PayloadHash = digest(original)
	expiry := retention.Expiry{ReferenceID: ref.ID, Rule: "source_imports.filename", PayloadHash: ref.PayloadHash, Revision: ref.Revision, Deadline: ref.Deadline, Storage: retention.Inline}
	projection, err := retention.ProjectClearedScalar(expiry, original, retention.EvidenceSummary{}, ref.ExpiredAt)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.Marshal(projection)
	ref.ProjectionHash = digest(proof)
	owner.Value = []byte{}
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err != nil {
		t.Fatal(err)
	}
	owner.Value = original
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("clear receipt left private scalar online")
	}
	owner.Value = []byte{}
	ref.Table, ref.Field = "generation_tasks", "frozen"
	owner.Table, owner.Field = ref.Table, ref.Field
	ref.ID, _ = retention.ReferenceID(ref.ProjectID, ref.Table, ref.OwnerKey, ref.Field)
	projection.ReferenceID = ref.ID
	proof, _ = json.Marshal(projection)
	ref.ProjectionHash = digest(proof)
	if _, err := validateExpiredArchiveReference(ref, owner, proof); err == nil {
		t.Fatal("clear scalar mode bypassed frozen contract")
	}
}
