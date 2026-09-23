package daemon

import "strings"

// cellProjects is a ledger cell's `projects` list, whether the cell came
// straight from the store ([]map[string]any) or back through JSON ([]any).
func cellProjects(cell map[string]any) []map[string]any {
	switch v := cell["projects"].(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// firstWS is key of a ledger cell's FIRST project, or nil when the cell
// names none — the single-id reading tests written before the list make.
func firstWS(cell map[string]any, key string) any {
	list := cellProjects(cell)
	if len(list) == 0 {
		return nil
	}
	return list[0][key]
}

// joinWS is every project id a cell names, comma-joined in order.
func joinWS(cell map[string]any) string {
	list := cellProjects(cell)
	ids := make([]string, 0, len(list))
	for _, w := range list {
		s, _ := w["project_id"].(string)
		ids = append(ids, s)
	}
	return strings.Join(ids, ",")
}
