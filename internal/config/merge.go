package config

// deepMerge recursively merges src into dst (map level).
// Maps: recursive merge. Slices/scalars: src replaces dst.
func deepMerge(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(dstMap, srcMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}
