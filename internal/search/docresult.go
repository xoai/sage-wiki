package search

// DocResult is the Docs-granularity wire shape the entry-point adapters
// emit (spec §2.1). Field names match the legacy hybrid.SearchResult JSON
// (ID/Content/Tags/ArticlePath/BM25Rank/VectorRank/RRFScore) so existing
// consumers keep parsing, plus the M3/M4 additions (SourceDate, GraphRank,
// AliasOf, FinalScore).
type DocResult struct {
	ID          string   `json:"ID"`
	Content     string   `json:"Content"`
	Tags        []string `json:"Tags,omitempty"`
	ArticlePath string   `json:"ArticlePath"`
	BM25Rank    int      `json:"BM25Rank"`
	VectorRank  int      `json:"VectorRank"`
	GraphRank   int      `json:"GraphRank,omitempty"`
	RRFScore    float64  `json:"RRFScore"`
	FinalScore  float64  `json:"FinalScore"`
	SourceDate  int64    `json:"SourceDate,omitempty"`
	AliasOf     string   `json:"AliasOf,omitempty"`
}

// DocResults maps unified results to the adapter wire shape.
func DocResults(rs []SearchResult) []DocResult {
	out := make([]DocResult, len(rs))
	for i, r := range rs {
		out[i] = DocResult{
			ID:          r.DocID,
			Content:     r.ChunkText,
			Tags:        r.Tags,
			ArticlePath: r.ArticlePath,
			BM25Rank:    r.BM25Rank,
			VectorRank:  r.VectorRank,
			GraphRank:   r.GraphRank,
			RRFScore:    r.RRFScore,
			FinalScore:  r.FinalScore,
			SourceDate:  r.SourceDate,
			AliasOf:     r.AliasOf,
		}
	}
	return out
}

// ParseChannels converts adapter-level channel names to Channels; unknown
// names are reported so adapters can reject them loudly.
func ParseChannels(names []string) ([]Channel, []string) {
	var chans []Channel
	var unknown []string
	for _, n := range names {
		switch Channel(n) {
		case ChannelBM25, ChannelVector, ChannelGraph:
			chans = append(chans, Channel(n))
		default:
			unknown = append(unknown, n)
		}
	}
	return chans, unknown
}
