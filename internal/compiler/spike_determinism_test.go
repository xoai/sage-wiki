package compiler

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// spikeDiffTrees walks both trees and returns one line per path whose bytes
// differ (or that exists on only one side), relative to the tree root.
func spikeDiffTrees(t *testing.T, dirA, dirB string) []string {
	t.Helper()
	filesA := spikeSnapshot(t, dirA)
	filesB := spikeSnapshot(t, dirB)

	var drift []string
	for rel, bytesA := range filesA {
		bytesB, ok := filesB[rel]
		if !ok {
			drift = append(drift, rel+" (only in A)")
			continue
		}
		if !bytes.Equal(bytesA, bytesB) {
			drift = append(drift, fmt.Sprintf("%s (%d vs %d bytes)", rel, len(bytesA), len(bytesB)))
		}
	}
	for rel := range filesB {
		if _, ok := filesA[rel]; !ok {
			drift = append(drift, rel+" (only in B)")
		}
	}
	return drift
}

func spikeSnapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}
