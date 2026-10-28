package integration

import (
	"context"
	"reflect"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func chapterEditableAgent(t *testing.T, ip *service.IP, actor domain.Actor, book service.NovelBook, chapter, role string) string {
	t.Helper()
	ctx := context.Background()
	svc := &service.NovelChapters{IP: ip}
	installed, err := svc.InstallAgents(ctx, actor.ID, book.ID, chapter)
	if err != nil || len(installed.Versions) != 4 {
		t.Fatal("chapter presets", err)
	}
	capabilities := &service.Capabilities{IP: ip}
	for _, ref := range installed.Versions {
		if ref.Kind != "agent" {
			continue
		}
		v, err := capabilities.Read(ctx, actor, book.ProjectID, ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		if v.Definition.Role != role {
			continue
		}
		if len(v.Definition.Dependencies) != 1 {
			t.Fatal("missing complete method")
		}
		definition := v.Definition
		definition.Instructions += "\n作者定制：保留场景节奏与原文证据。"
		edited, err := capabilities.Save(ctx, actor, book.ProjectID, service.CapabilitySave{OperationID: "chapter-edit-" + role, ExpectedHead: v.ID, Definition: definition})
		if err != nil {
			t.Fatal("edit chapter agent", err)
		}
		again, err := svc.InstallAgents(ctx, actor.ID, book.ID, chapter)
		if err != nil || !reflect.DeepEqual(installed, again) {
			t.Fatal("preset replay", err)
		}
		definition.Instructions += "\n后续修订。"
		if _, err = capabilities.Save(ctx, actor, book.ProjectID, service.CapabilitySave{OperationID: "chapter-after-install-" + role, ExpectedHead: edited.ID, Definition: definition}); err != nil {
			t.Fatal("installation reset edited head", err)
		}
		return edited.ID
	}
	t.Fatal("missing role", role)
	return ""
}
