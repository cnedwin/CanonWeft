package retention

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
)

func (b *contextSourceBuilder) novelChapterPrompt(ctx context.Context, ref *domain.NovelChapterSource, role string, use *domain.CapabilityUse, request models.Request, read ReadSourceObject) error {
	if ref == nil {
		return nil
	}
	if !id32.MatchString(ref.BookID) || !id32.MatchString(ref.ResourceID) || ref.Revision < 1 || !hash64.MatchString(ref.Hash) || (ref.BaseHead != "" && !id32.MatchString(ref.BaseHead)) {
		return ErrSourceOwnership
	}
	book, err := b.index.sourceRow("novels", map[string]string{"id": ref.BookID})
	if err != nil || book.Values["project_id"] != b.project || book.Values["branch_id"] != b.branch {
		return ErrSourceOwnership
	}
	r, err := b.index.sourceRow("resources", map[string]string{"id": ref.ResourceID})
	if err != nil || r.Values["project_id"] != b.project || r.Values["branch_id"] != b.branch || r.Values["kind"] != "novel_chapter_control" {
		return ErrSourceOwnership
	}
	var input domain.NovelChapterRequest
	prompt := request.Prompt
	if use != nil {
		// capabilityPrompt has already verified the full immutable method frame,
		// schema, manifest and package sources before this chapter-specific check.
		if use.AgentVersionID == "" || (use.Mode != "TRIAL" && use.Mode != "QUALIFIED") {
			return ErrSourceOwnership
		}
		open, close, _ := models.CapabilityMethodFrame(use.Mode == "TRIAL")
		start := strings.LastIndex(prompt, open)
		if start < 0 {
			return ErrSourceOwnership
		}
		start += len(open)
		end := strings.Index(prompt[start:], close)
		if end < 0 {
			return ErrSourceOwnership
		}
		var payload capabilityPromptData
		if authorDocument(prompt[start:start+end], &payload) != nil {
			return ErrSourceOwnership
		}
		prompt = string(payload.Input)
	}
	d := json.NewDecoder(strings.NewReader(prompt))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || (role != "writer" && role != "bookkeeper") || (role == "writer" && len(input.Plans) != 4) || (role == "bookkeeper" && len(input.Plans) != 0) {
		return ErrSourceOwnership
	}
	encoded, err := json.Marshal(input)
	if err != nil || (use == nil && !strings.HasPrefix(prompt, string(encoded)+"\n\n<reference-data>\n")) {
		return ErrSourceOwnership
	}
	if use != nil {
		// The method renderer normalizes JSON key order, including RawMessage.
		// Reconstruct the exact typed chapter serialization used by Save, while
		// rejecting unknown/duplicate fields, changed text and numeric changes.
		if authorDocument(prompt, &input) != nil {
			return ErrSourceOwnership
		}
	}
	chapter, materials, err := chapterResearchSourceInput(input.Draft, ref.Hash)
	if err != nil {
		return err
	}
	var draftFields map[string]json.RawMessage
	if authorDocument(string(input.Draft), &draftFields) != nil {
		return ErrSourceOwnership
	}
	if _, hasCard := draftFields["writing_card"]; hasCard {
		if role != "writer" {
			return ErrSourceOwnership
		}
		settings, e := b.index.sourceRow("novel_settings_versions", map[string]string{"novel_id": ref.BookID, "revision": book.Values["settings_revision"]})
		var setup novel.Setup
		if e != nil || contentJSON([]byte(settings.Values["payload"]), &setup, settings.Values["content_hash"]) != nil || validateChapterWritingCard(input.Draft, setup, chapter, input.Plans) != nil {
			return ErrSourceOwnership
		}
		if e = b.link(settings, "payload", ""); e != nil {
			return e
		}
	}
	for i, material := range materials {
		citation := chapter.References[i]
		if material.Dependency.Source != citation.Source || material.Dependency.Kind != "evidence" || citation.Source.BranchID != b.branch || material.Hidden != (material.Space == "working") || (material.Space != "working" && material.Space != "evidence") || !strings.Contains(material.Text, citation.Quote) {
			return ErrSourceOwnership
		}
		if err = b.required(ctx, material, read); err != nil {
			return err
		}
	}
	manifest := domain.PrivateManifest{Project: b.project, Branch: b.branch, Actor: b.actor}
	pin := domain.PrivatePin{Ref: domain.WorkingRef{Kind: "resource_draft", ID: ref.ResourceID, Version: strconv.FormatInt(ref.Revision, 10)}, State: "PRIVATE_DRAFT", BaseHead: ref.BaseHead, Part: 0, Name: r.Values["name"], Format: "text", Hash: ref.Hash}
	source, err := b.index.privateInputSource(b.project, manifest, pin)
	if err != nil {
		return err
	}
	if err = appendSourceRequest(b.m, source); err != nil {
		return err
	}
	for _, plan := range input.Plans {
		if !plan.Hidden || plan.Space != "working" || plan.Dependency.Source.BranchID != b.branch {
			return ErrSourceOwnership
		}
		if err = b.required(ctx, plan, read); err != nil {
			return err
		}
	}
	return nil
}

func chapterResearchSourceInput(raw []byte, hash string) (novel.ChapterDocument, []domain.RequiredInput, error) {
	var chapter novel.ChapterDocument
	var fields map[string]json.RawMessage
	var materials []domain.RequiredInput
	if authorDocument(string(raw), &fields) != nil || fields == nil {
		return chapter, nil, ErrSourceOwnership
	}
	if value, ok := fields["reference_materials"]; ok {
		if authorDocument(string(value), &materials) != nil {
			return chapter, nil, ErrSourceOwnership
		}
		delete(fields, "reference_materials")
	}
	// The caller independently re-derives and verifies this metadata against
	// owned settings and complete plans before accepting its source lineage.
	delete(fields, "writing_card")
	plain, err := json.Marshal(fields)
	if err != nil || contentJSON(plain, &chapter, hash) != nil || len(chapter.References) != len(materials) {
		return chapter, nil, ErrSourceOwnership
	}
	return chapter, materials, nil
}

func validateChapterWritingCard(raw []byte, setup novel.Setup, chapter novel.ChapterDocument, plans []domain.RequiredInput) error {
	var fields map[string]json.RawMessage
	if authorDocument(string(raw), &fields) != nil {
		return ErrSourceOwnership
	}
	value, exists := fields["writing_card"]
	if !exists {
		return nil
	}
	var actual novel.WritingCard
	if authorDocument(string(value), &actual) != nil {
		return ErrSourceOwnership
	}
	expected, err := novel.BuildWritingCard(setup, chapter, plans)
	if err != nil || stableHash(actual) != stableHash(expected) {
		return ErrSourceOwnership
	}
	return nil
}
