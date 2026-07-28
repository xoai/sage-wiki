package memory

import (
	"fmt"
	"testing"
)

// GetMany batches its IN clause: past sqlite's 999-variable limit an
// unbounded clause errors the whole call, and the facade would then hydrate
// nothing on a large result set (F-067's failure mode, batch-hydration twin).
func TestGetManyBatchesLargeIDSets(t *testing.T) {
	_, store := setupTestDB(t)

	ids := make([]string, 1200)
	for i := range ids {
		ids[i] = fmt.Sprintf("doc%d", i)
		if i%3 == 0 {
			if err := store.Add(Entry{ID: ids[i], Content: fmt.Sprintf("content %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
	}

	got, err := store.GetMany(ids)
	if err != nil {
		t.Fatalf("batched lookup failed: %v", err)
	}
	if len(got) != 400 {
		t.Errorf("got %d entries, want 400", len(got))
	}
	if e := got["doc300"]; e == nil || e.Content != "content 300" {
		t.Errorf("doc300 = %+v, want content %q", e, "content 300")
	}
	if _, ok := got["doc301"]; ok {
		t.Error("doc301 was never added — it must be absent from the map")
	}
}
