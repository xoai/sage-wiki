package embed

import "testing"

// TestAPIEmbedder_SetDimsOnce pins the auto-detect contract: first write
// wins; later calls don't overwrite (and must not race — this is the
// mutex's reason to exist).
func TestAPIEmbedder_SetDimsOnce(t *testing.T) {
	e := &APIEmbedder{model: "m"}
	e.setDims(768)
	e.setDims(1024)
	if got := e.Dimensions(); got != 768 {
		t.Errorf("Dimensions() = %d, want 768 (first write wins)", got)
	}
}

// TestOllamaEmbedder_SetDimsOnce pins the same guard on the sibling struct.
func TestOllamaEmbedder_SetDimsOnce(t *testing.T) {
	e := &OllamaEmbedder{model: "m"}
	e.setDims(768)
	e.setDims(1024)
	if got := e.Dimensions(); got != 768 {
		t.Errorf("Dimensions() = %d, want 768 (first write wins)", got)
	}
}
