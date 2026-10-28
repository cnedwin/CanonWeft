package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/novel"
	"novelstudio/internal/storage"
	"testing"
)

func assertBookReviewRestore(t *testing.T, source, restored *sql.DB, blobs storage.LocalBlobs, project string) {
	t.Helper()
	rows, err := source.Query(`SELECT s.id,s.input_hash,s.output_hash,JSON_UNQUOTE(JSON_EXTRACT(s.frozen,'$.input.context_id')) FROM recipe_steps s JOIN recipe_runs r ON r.id=s.run_id WHERE r.project_id=? AND JSON_UNQUOTE(JSON_EXTRACT(r.frozen,'$.input.kind'))='novel-book-review-v1' AND s.active=TRUE AND s.state='COMPLETED' ORDER BY s.id`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, input, output, contextID string
		if err = rows.Scan(&id, &input, &output, &contextID); err != nil {
			t.Fatal(err)
		}
		var copiedInput, copiedOutput, originalContext, copiedContext string
		if err = restored.QueryRow("SELECT input_hash,output_hash FROM recipe_steps WHERE id=?", id).Scan(&copiedInput, &copiedOutput); err != nil || input != copiedInput || output != copiedOutput {
			t.Fatal("review source/result pins lost", err)
		}
		if err = source.QueryRow("SELECT payload FROM context_packages WHERE id=?", contextID).Scan(&originalContext); err != nil {
			t.Fatal(err)
		}
		if err = restored.QueryRow("SELECT payload FROM context_packages WHERE id=?", contextID).Scan(&copiedContext); err != nil || copiedContext != originalContext {
			t.Fatal("review full text context lost", err)
		}
		raw, e := blobs.ReadObject(context.Background(), project, output)
		if e != nil {
			t.Fatal(e)
		}
		var result novel.BookReviewResult
		if json.Unmarshal(raw, &result) != nil || len(result.Coverage) == 0 {
			t.Fatal("review report object unreadable")
		}
		count++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count > 0 && count != 2 {
		t.Fatal("review restored coverage", count)
	}
}
