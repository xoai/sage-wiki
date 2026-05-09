package trust

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

type IndexStores struct {
	MemStore   *memory.Store
	VecStore   *vectors.Store
	OntStore   *ontology.Store
	ChunkStore *memory.ChunkStore
	Embedder   embed.Embedder
	DB         *storage.DB
	ChunkSize  int
}

func PromoteOutput(store *Store, id string, projectDir string, stores IndexStores) error {
	o, err := store.Get(id)
	if err != nil {
		return err
	}

	docID := "output:" + id
	relPath := o.FilePath

	// Move file from under_review/ to outputs/ BEFORE marking confirmed
	if strings.Contains(relPath, "/under_review/") {
		newRelPath := strings.Replace(relPath, "/under_review/", "/outputs/", 1)
		oldAbs := filepath.Join(projectDir, relPath)
		newAbs := filepath.Join(projectDir, newRelPath)
		os.MkdirAll(filepath.Dir(newAbs), 0755)
		if err := os.Rename(oldAbs, newAbs); err != nil {
			return fmt.Errorf("trust: move to outputs: %w", err)
		}
		relPath = newRelPath
		if err := store.UpdateFilePath(id, newRelPath); err != nil {
			return fmt.Errorf("trust: update file path: %w", err)
		}
	}

	// Index into search stores BEFORE marking confirmed
	if err := stores.MemStore.Add(memory.Entry{
		ID:          docID,
		Content:     o.Answer,
		Tags:        []string{"output"},
		ArticlePath: relPath,
	}); err != nil {
		return fmt.Errorf("trust: FTS5 index: %w", err)
	}

	if stores.Embedder != nil {
		if vec, err := stores.Embedder.Embed(o.Answer); err == nil {
			stores.VecStore.Upsert(docID, vec)
		}
	}

	stores.OntStore.AddEntity(ontology.Entity{
		ID:          docID,
		Type:        ontology.TypeArtifact,
		Name:        o.Question,
		ArticlePath: relPath,
	})

	var sources []string
	if o.SourcesUsed != "" {
		json.Unmarshal([]byte(o.SourcesUsed), &sources)
	}
	for _, src := range sources {
		conceptID := strings.TrimSuffix(filepath.Base(src), ".md")
		stores.OntStore.AddRelation(ontology.Relation{
			ID:       docID + "-derived-" + conceptID,
			SourceID: docID,
			TargetID: conceptID,
			Relation: ontology.RelDerivedFrom,
		})
	}

	if stores.ChunkStore != nil && stores.DB != nil {
		chunkSize := stores.ChunkSize
		if chunkSize <= 0 {
			chunkSize = 800
		}
		chunks := extract.ChunkText(o.Answer, chunkSize)

		var chunkEmbeddings [][]float32
		if stores.Embedder != nil {
			chunkEmbeddings = make([][]float32, len(chunks))
			for i, c := range chunks {
				if vec, err := stores.Embedder.Embed(c.Text); err == nil {
					chunkEmbeddings[i] = vec
				}
			}
		}

		if err := stores.DB.WriteTx(func(tx *sql.Tx) error {
			stores.ChunkStore.DeleteDocChunks(tx, docID)
			entries := make([]memory.ChunkEntry, len(chunks))
			for i, c := range chunks {
				entries[i] = memory.ChunkEntry{
					ChunkID:    fmt.Sprintf("%s:c%d", docID, i),
					ChunkIndex: c.Index,
					Heading:    c.Heading,
					Content:    c.Text,
				}
			}
			if err := stores.ChunkStore.IndexChunks(tx, docID, entries); err != nil {
				return err
			}
			if chunkEmbeddings != nil {
				for i, emb := range chunkEmbeddings {
					if emb != nil {
						stores.VecStore.UpsertChunk(tx, entries[i].ChunkID, docID, emb)
					}
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("trust: chunk indexing: %w", err)
		}
	}

	// Mark confirmed LAST — only after all artifacts are durable
	if err := store.Promote(id); err != nil {
		return fmt.Errorf("trust: mark confirmed: %w", err)
	}

	return nil
}

func DemoteOutput(store *Store, id string, stores IndexStores) error {
	if err := store.Demote(id); err != nil {
		return err
	}

	docID := "output:" + id

	stores.MemStore.Delete(docID)
	stores.VecStore.Delete(docID)
	stores.OntStore.DeleteEntity(docID)

	if stores.ChunkStore != nil && stores.DB != nil {
		stores.DB.WriteTx(func(tx *sql.Tx) error {
			return stores.ChunkStore.DeleteDocChunks(tx, docID)
		})
	}

	return nil
}

func DeindexOutput(id string, stores IndexStores) {
	docID := "output:" + id
	stores.MemStore.Delete(docID)
	stores.VecStore.Delete(docID)
	stores.OntStore.DeleteEntity(docID)

	if stores.ChunkStore != nil && stores.DB != nil {
		stores.DB.WriteTx(func(tx *sql.Tx) error {
			return stores.ChunkStore.DeleteDocChunks(tx, docID)
		})
	}
}

func RejectOutput(store *Store, id string, projectDir string, stores IndexStores) error {
	o, err := store.Get(id)
	if err != nil {
		return err
	}

	DeindexOutput(id, stores)

	if o.FilePath != "" {
		absPath := filepath.Join(projectDir, o.FilePath)
		os.Remove(absPath)
	}

	return store.Delete(id)
}
