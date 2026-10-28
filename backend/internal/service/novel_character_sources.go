package service

import (
	"context"
	"database/sql"
	"fmt"
	"novelstudio/internal/domain"
)

// Selection records author intent. Preparing a generation freezes the complete
// current content and its dependency versions in the existing context package.
func validateCharacterSourcesTx(ctx context.Context, tx *sql.Tx, book NovelBook, ids []string) error {
	if len(ids) > 12 {
		return domain.ErrValidation
	}
	if err := validateRequiredIDs(ids); err != nil {
		return err
	}
	for _, id := range ids {
		r, err := resourceTx(ctx, tx, book.ProjectID, id)
		if err != nil {
			return err
		}
		if r.BranchID != book.BranchID || !allowsRequiredPolicy(r.Policy, SearchInput{Purpose: "author", IncludeWorking: true}) {
			return domain.ErrForbidden
		}
		if r.Head == "" || isNovelPlan(r.Kind) {
			return fmt.Errorf("%w：请选择本书已采用的参考资料；世界与故事规划由系统自动加入", domain.ErrNotReady)
		}
	}
	return nil
}

func validateCharacterSourceContext(ids []string, pack domain.ContextPackage) error {
	for _, id := range ids {
		found := false
		for _, item := range pack.Required {
			ref := item.Dependency.Source
			if ref.Kind == "resource" && ref.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w：角色创作缺少所选资料全文，请重新准备任务", domain.ErrNotReady)
		}
	}
	return nil
}
