package retention

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixtureInventory() (Inventory, time.Time) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	return Inventory{ProjectID: strings.Repeat("a", 32), Revision: 3, CatalogVersion: 1, CoveredTables: KnownTables(), CapturedAt: now, Complete: true, Objects: []Object{{Hash: strings.Repeat("b", 64), State: ObjectPresent, Revision: 1}}}, now
}
func fixtureRef(id, rule, hash string, born time.Time) Reference {
	return Reference{ID: strings.Repeat(id, 64), Rule: rule, ObjectHash: hash, PayloadHash: strings.Repeat("f", 64), CreatedAt: born, TimeBasis: SourceUTC, Revision: 1, State: Retained}
}

func TestPlanAcceptsAbsentExpiredRecoveryObjectWithoutInventingDeletion(t *testing.T) {
	inv, now := fixtureInventory()
	ref := fixtureRef("1", "artifact_releases.content_hash", strings.Repeat("b", 64), now.Add(-90*day))
	ref.State, ref.ExpiredAt = Expired, now.Add(-day)
	inv.Objects, inv.References = nil, []Reference{ref}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 0 || len(plan.Expire) != 0 {
		t.Fatal("expired history requires an invented physical deletion", plan, err)
	}
	inv.References[0].State, inv.References[0].ExpiredAt = Retained, time.Time{}
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("missing retained object accepted")
	}
}

func TestPlanExpiresRawReferenceWithoutDeletingSharedPublishedContent(t *testing.T) {
	inv, now := fixtureInventory()
	hash := strings.Repeat("b", 64)
	raw := fixtureRef("1", "model_calls.output_hash", hash, now.Add(-30*24*time.Hour))
	content := fixtureRef("2", "resources.content_hash", hash, now.Add(-60*24*time.Hour))
	content.ContentRoot = true
	inv.References = []Reference{raw, content}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 1 || plan.Expire[0].ReferenceID != raw.ID || len(plan.DeleteObjects) != 0 {
		t.Fatal("raw expiry broke published object", plan, err)
	}
	if plan.Expire[0].PayloadHash != raw.PayloadHash || plan.Expire[0].Rule != raw.Rule {
		t.Fatal("expiry lost original evidence identity")
	}
}

func TestPlanExpiresInlinePreviewMetricsAndRawBlobsAtBoundary(t *testing.T) {
	inv, now := fixtureInventory()
	for i, rule := range []string{"generation_tasks.frozen.input_hash", "generation_previews.content", "qualification_steps.metrics", "qualification_runs.report_hash", "semantic_review_runs.report_hash"} {
		hash := strings.Repeat("b", 64)
		if i == 1 || i == 2 {
			hash = ""
		}
		inv.References = append(inv.References, fixtureRef(string(rune('1'+i)), rule, hash, now.Add(-30*24*time.Hour)))
	}
	before, err := BuildPlan(inv, now.Add(-time.Nanosecond))
	if err == nil {
		t.Fatal("plan accepted cutoff older than inventory", before)
	}
	inv.CapturedAt = now.Add(-time.Second)
	before, err = BuildPlan(inv, now.Add(-time.Nanosecond))
	if err != nil || len(before.Expire) != 0 {
		t.Fatal("expired before precise deadline", before, err)
	}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 5 || len(plan.DeleteObjects) != 1 {
		t.Fatal("raw/inline references not fully expired or object deletion duplicated", plan, err)
	}
	if len(plan.Hash) != 64 {
		t.Fatal("plan has no stable integrity binding")
	}
}

func TestPlanRejectsIncompleteUnknownOrMisclassifiedInventory(t *testing.T) {
	for _, kind := range []string{"incomplete", "missing table", "unknown table", "unknown rule", "wrong hash", "duplicate reference", "raw promotion", "fabricated lifecycle", "expired without date"} {
		t.Run(kind, func(t *testing.T) {
			inv, now := fixtureInventory()
			inv.References = []Reference{fixtureRef("1", "model_calls.output_hash", strings.Repeat("b", 64), now.Add(-31*24*time.Hour))}
			switch kind {
			case "incomplete":
				inv.Complete = false
			case "missing table":
				inv.CoveredTables = inv.CoveredTables[1:]
			case "unknown table":
				inv.CoveredTables = append(inv.CoveredTables, "future_private_materials")
			case "unknown rule":
				inv.References[0].Rule = "model_calls.input_hash"
			case "wrong hash":
				inv.References[0].ObjectHash = "../private"
			case "duplicate reference":
				inv.References = append(inv.References, inv.References[0])
			case "raw promotion":
				inv.References[0].ContentRoot = true
			case "fabricated lifecycle":
				inv.Lifecycle.DeletedAt = now.Add(-40 * 24 * time.Hour)
			case "expired without date":
				inv.References[0].State = Expired
			}
			if _, err := BuildPlan(inv, now); err == nil {
				t.Fatal("unsafe inventory produced deletion plan")
			}
		})
	}
}

func TestPlanIsCanonicalAndPinsInventoryAndReferenceRevisions(t *testing.T) {
	inv, now := fixtureInventory()
	inv.References = []Reference{fixtureRef("2", "model_calls.output_hash", strings.Repeat("b", 64), now.Add(-31*24*time.Hour)), fixtureRef("1", "qualification_steps.metrics", "", now.Add(-31*24*time.Hour))}
	a, err := BuildPlan(inv, now)
	if err != nil {
		t.Fatal(err)
	}
	inv.References[0], inv.References[1] = inv.References[1], inv.References[0]
	b, err := BuildPlan(inv, now)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("input order changed plan", err)
	}
	inv.Revision++
	c, _ := BuildPlan(inv, now)
	if c.Hash == a.Hash {
		t.Fatal("inventory concurrency revision missing from hash")
	}
	inv.Revision--
	inv.References[0].Revision++
	d, _ := BuildPlan(inv, now)
	if d.Hash == a.Hash {
		t.Fatal("reference concurrency revision missing from hash")
	}
}

func TestPlanDoesNotReactivateExpiredEvidenceAndHonorsBoundedLease(t *testing.T) {
	inv, now := fixtureInventory()
	r := fixtureRef("1", "generation_tasks.frozen.input_hash", strings.Repeat("b", 64), now.Add(-31*24*time.Hour))
	r.State = Expired
	r.ExpiredAt = now.Add(-time.Hour)
	r.LeaseUntil = now.Add(time.Minute)
	inv.References = []Reference{r}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 0 || len(plan.DeleteObjects) != 0 || len(plan.Blocked) != 1 {
		t.Fatal("expiry reactivated or lease ignored", plan, err)
	}
	inv.References[0].LeaseUntil = now.Add(25 * time.Hour)
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("unbounded lease accepted")
	}
	inv.References[0].LeaseUntil = time.Time{}
	plan, err = BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 1 {
		t.Fatal("expired payload never becomes reclaimable", plan, err)
	}
}

func TestPlanRequiresKnownObjectStateAndDoesNotRepeatCompletedDeletion(t *testing.T) {
	inv, now := fixtureInventory()
	r := fixtureRef("1", "model_calls.output_hash", strings.Repeat("b", 64), now.Add(-31*24*time.Hour))
	inv.References = []Reference{r}
	inv.Objects = nil
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("missing object inventory accepted")
	}
	inv.Objects = []Object{{Hash: r.ObjectHash, State: ObjectDeleted, Revision: 2}}
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("retained reference to deleted object accepted")
	}
	inv.References[0].State = Expired
	inv.References[0].ExpiredAt = now.Add(-time.Hour)
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 0 {
		t.Fatal("completed object deletion scheduled again", plan, err)
	}
}

func TestPlanKeepsUnresolvedAccountingButExpiresItsRawContent(t *testing.T) {
	inv, now := fixtureInventory()
	raw := fixtureRef("1", "model_calls.output_hash", strings.Repeat("b", 64), now.Add(-400*24*time.Hour))
	account := fixtureRef("2", "model_calls.accounting", "", raw.CreatedAt)
	account.AccountingOpen = true
	inv.References = []Reference{raw, account}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 1 || plan.Expire[0].ReferenceID != raw.ID || len(plan.DeleteObjects) != 1 {
		t.Fatal("UNKNOWN cost was lost or retained private output", plan, err)
	}
}

func TestPlanLegacyClockUsesFirstVerifiedRegistrationAndNeverOriginalWallClock(t *testing.T) {
	inv, now := fixtureInventory()
	r := fixtureRef("1", "model_calls.output_hash", strings.Repeat("b", 64), now.Add(-400*24*time.Hour))
	r.TimeBasis = LegacyRegistered
	r.FirstVerifiedAt = now
	inv.References = []Reference{r}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 0 {
		t.Fatal("legacy wall clock caused immediate deletion", plan, err)
	}
	plan, err = BuildPlan(inv, now.Add(30*24*time.Hour))
	if err != nil || len(plan.Expire) != 1 {
		t.Fatal("legacy grace never becomes eligible", plan, err)
	}
	inv.References[0].FirstVerifiedAt = time.Time{}
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("unregistered legacy timestamp accepted")
	}
	inv.References[0].TimeBasis = ""
	if _, err := BuildPlan(inv, now); err == nil {
		t.Fatal("unclassified source time accepted")
	}
}

func TestOrphanObjectsNeedTrustedAgeCompleteInventoryAndNoWriterLease(t *testing.T) {
	inv, now := fixtureInventory()
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 0 || len(plan.Blocked) != 1 {
		t.Fatal("unknown-age orphan is not explicitly blocked", plan, err)
	}
	inv.Objects[0].FirstSeenAt = now.Add(-30 * day)
	plan, err = BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 1 {
		t.Fatal("trusted registered orphan never becomes eligible", plan, err)
	}
	inv.Objects[0].FirstSeenAt = now.Add(-30*day + time.Nanosecond)
	plan, err = BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 0 {
		t.Fatal("orphan expired before registration grace", plan, err)
	}
	inv.Objects[0].FirstSeenAt = now.Add(-31 * day)
	inv.Objects[0].LeaseUntil = now.Add(time.Minute)
	plan, err = BuildPlan(inv, now)
	if err != nil || len(plan.DeleteObjects) != 0 || len(plan.Blocked) != 1 {
		t.Fatal("writer lease ignored", plan, err)
	}
	inv.Objects[0].LeaseUntil = now.Add(25 * time.Hour)
	if _, err = BuildPlan(inv, now); err == nil {
		t.Fatal("unbounded object lease accepted")
	}
	inv.Objects[0].LeaseUntil = time.Time{}
	inv.Complete = false
	if _, err = BuildPlan(inv, now); err == nil {
		t.Fatal("incomplete scan authorized orphan deletion")
	}
	inv.Complete = true
	inv.Objects[0].FirstSeenAt = now.Add(time.Second)
	if _, err = BuildPlan(inv, now); err == nil {
		t.Fatal("future object registration accepted")
	}
}

func TestObjectWriterLeaseAlsoProtectsObjectWithExpiredReferences(t *testing.T) {
	inv, now := fixtureInventory()
	inv.Objects[0].LeaseUntil = now.Add(time.Minute)
	inv.References = []Reference{fixtureRef("1", "model_calls.output_hash", inv.Objects[0].Hash, now.Add(-31*day))}
	plan, err := BuildPlan(inv, now)
	if err != nil || len(plan.Expire) != 1 || len(plan.DeleteObjects) != 0 || len(plan.Blocked) != 1 {
		t.Fatal("physical lease bypassed by due reference", plan, err)
	}
}
