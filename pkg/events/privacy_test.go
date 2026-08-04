package events

import (
	"reflect"
	"strings"
	"testing"
)

// TestEventUnionPrivacySchema (SPEC-07 AC-5): iterate ALL payload types —
// no field that carries document content, no absolute-path field, and the
// envelope rejects path workspaces. The audit is structural (field names +
// types), so a future payload addition that smuggles in a body field fails
// here at compile-review time.
func TestEventUnionPrivacySchema(t *testing.T) {
	forbidden := map[string]bool{
		"content": true, "body": true, "text": true, "raw": true,
		"dir": true, "path": true, "filepath": true, "abs_path": true,
	}
	types := []Type{
		TypeDocCaptured, TypeCompileStarted, TypeCompileDocFinished,
		TypeCompileFinished, TypeEdgeAdded, TypeEdgeInvalidated,
		TypeEntityResolved, TypePromotionTriggered, TypeSearchPerformed,
		TypeMirrorShipped, TypeMirrorSnapshot, TypeUsage,
		TypeCompileSkip, TypeEventsDropped,
	}
	for _, typ := range types {
		pt := PayloadType(typ)
		if pt == nil {
			t.Errorf("%s has no payload type", typ)
			continue
		}
		walkFields(pt, func(f reflect.StructField) {
			name := strings.ToLower(f.Name)
			if forbidden[name] {
				// SearchPerformed.Query is the ONE opt-in raw field
				// (events.raw_queries) — hashed by default, empty otherwise.
				if typ != TypeSearchPerformed || f.Name != "Query" {
					t.Errorf("%s payload has forbidden field %s (document content/path carrier)", typ, f.Name)
				}
			}
			if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Uint8 {
				t.Errorf("%s payload has a []byte field %s (raw content carrier)", typ, f.Name)
			}
		})
	}

	// The envelope rejects path workspaces mechanically.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("NewEvent must panic on a path workspace")
			}
		}()
		NewEvent("/abs/ws", TypeEventsDropped, EventsDropped{Dropped: 1})
	}()
}

// walkFields visits every field of t, RECURSING into nested structs (and
// pointers to structs) — a path/content field hidden inside a nested
// payload (UsageSummary, CompileTotals) must not slip past the audit.
func walkFields(t reflect.Type, fn func(reflect.StructField)) {
	walkFieldsRec(t, fn, map[reflect.Type]bool{})
}

func walkFieldsRec(t reflect.Type, fn func(reflect.StructField), seen map[reflect.Type]bool) {
	if seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fn(f)
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() == "github.com/xoai/sage-wiki/pkg/events" {
			walkFieldsRec(ft, fn, seen)
		}
	}
}
