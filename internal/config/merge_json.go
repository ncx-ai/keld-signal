// internal/config/merge_json.go
package config

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/iancoleman/orderedmap"

	"github.com/ncx-ai/keld-signal/internal/errs"
)

// LoadJSON parses JSON text into an order-preserving map.
// Blank/empty input returns an empty map; invalid JSON returns an errs.Error.
func LoadJSON(text string) (*orderedmap.OrderedMap, error) {
	o := orderedmap.New()
	if strings.TrimSpace(text) == "" {
		return o, nil
	}
	if err := json.Unmarshal([]byte(text), o); err != nil {
		return nil, errs.New("existing config is not valid JSON: %v", err)
	}
	return o, nil
}

// normalizeEscape recursively disables orderedmap's per-instance HTML escaping
// so DumpJSON/marshalCompact match Python's json.dumps for &, <, > at any depth.
// The library stores a per-instance escapeHTML flag (default true) whose
// MarshalJSON does NOT inherit the parent encoder's SetEscapeHTML setting, so
// each map (including value-form maps produced by Unmarshal, and maps inside
// arrays) must be normalized individually. Returns the (possibly replaced)
// value; every container writes back the returned child so there is no stale
// pre-recursion copy. The maps are mutated in place — fine, since they are
// being serialized and callers do not reuse the object afterward.
func normalizeEscape(v any) any {
	switch m := v.(type) {
	case *orderedmap.OrderedMap:
		m.SetEscapeHTML(false)
		for _, k := range m.Keys() {
			cv, _ := m.Get(k)
			m.Set(k, normalizeEscape(cv))
		}
		return m
	case orderedmap.OrderedMap:
		m.SetEscapeHTML(false)
		for _, k := range m.Keys() {
			cv, _ := m.Get(k)
			m.Set(k, normalizeEscape(cv))
		}
		return m
	case []interface{}:
		for i := range m {
			m[i] = normalizeEscape(m[i])
		}
		return m
	default:
		return v
	}
}

// DumpJSON serialises obj to a JSON string with 2-space indent and a trailing
// newline, matching Python's json.dumps(obj, indent=2) + "\n". HTML escaping
// is disabled at every level to match Python's default (Python does not escape
// &, <, >).
func DumpJSON(obj *orderedmap.OrderedMap) string {
	norm := normalizeEscape(obj)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // match Python json.dumps (no &,<,> escaping)
	enc.SetIndent("", "  ")
	_ = enc.Encode(norm) // Encode appends a trailing newline
	return buf.String()
}

// asMap coerces an interface{} value to *orderedmap.OrderedMap, handling both
// the value and pointer forms that orderedmap may produce during unmarshal.
func asMap(v any) (*orderedmap.OrderedMap, bool) {
	switch m := v.(type) {
	case orderedmap.OrderedMap:
		return &m, true
	case *orderedmap.OrderedMap:
		return m, true
	}
	return nil, false
}

// subMap returns the *orderedmap.OrderedMap stored at key, or a new empty map
// if the key is absent or not a map. Callers must store the result back via
// obj.Set if they mutate it.
func subMap(obj *orderedmap.OrderedMap, key string) *orderedmap.OrderedMap {
	if v, ok := obj.Get(key); ok {
		if sm, ok := asMap(v); ok {
			return sm
		}
	}
	return orderedmap.New()
}

// marshalCompact returns the compact JSON representation of v with HTML
// escaping disabled, matching Python's json.dumps (which does not escape
// &, <, >). Used for idempotency checks and substring tests so they are
// byte-faithful to the Python CLI.
func marshalCompact(v any) string {
	v = normalizeEscape(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(buf.String(), "\n")
}

// MergeEnv upserts keys from env into the "env" sub-object of obj, preserving
// existing key order. Returns the list of keys from env (in iteration order).
func MergeEnv(obj *orderedmap.OrderedMap, env *orderedmap.OrderedMap) []string {
	sec := subMap(obj, "env")
	var keys []string
	for _, k := range env.Keys() {
		v, _ := env.Get(k)
		sec.Set(k, v)
		keys = append(keys, k)
	}
	obj.Set("env", sec)
	return keys
}

// RemoveSectionKeys removes the listed keys from the named sub-object of obj.
// If the sub-object becomes empty after removal, it is deleted from obj.
func RemoveSectionKeys(obj *orderedmap.OrderedMap, section string, keys []string) {
	v, ok := obj.Get(section)
	if !ok {
		return
	}
	sec, ok := asMap(v)
	if !ok {
		return
	}
	for _, k := range keys {
		sec.Delete(k)
	}
	if len(sec.Keys()) == 0 {
		obj.Delete(section)
	} else {
		obj.Set(section, sec)
	}
}

// AddClaudeHook appends a hook entry to obj["hooks"][event] if not already
// present (idempotent). The entry is:
//
//	{ "matcher": matcher (if non-nil), "hooks": [{"type": "command", "command": command}] }
//
// Key order within the entry matches Python's dict literal order so that
// golden JSON output is byte-identical to the Python CLI.
func AddClaudeHook(obj *orderedmap.OrderedMap, event string, matcher *string, command string) {
	// Build inner hook object: {type: "command", command: command}
	inner := orderedmap.New()
	inner.Set("type", "command")
	inner.Set("command", command)

	// Build entry: {matcher? …, hooks: [{…}]}
	entry := orderedmap.New()
	if matcher != nil {
		entry.Set("matcher", *matcher)
	}
	entry.Set("hooks", []any{inner})

	// Obtain or create obj["hooks"]
	var hooksMap *orderedmap.OrderedMap
	if hv, ok := obj.Get("hooks"); ok {
		if hm, ok := asMap(hv); ok {
			hooksMap = hm
		} else {
			hooksMap = orderedmap.New()
		}
	} else {
		hooksMap = orderedmap.New()
	}

	// Obtain or create hooksMap[event] array
	var arr []any
	if av, ok := hooksMap.Get(event); ok {
		if slice, ok := av.([]interface{}); ok {
			arr = slice
		}
	}

	// Idempotency: skip if an equivalent entry already exists (compare compact JSON)
	entryJSON := marshalCompact(entry)
	for _, existing := range arr {
		if marshalCompact(existing) == entryJSON {
			return
		}
	}

	arr = append(arr, entry)
	hooksMap.Set(event, arr)
	obj.Set("hooks", hooksMap)
}

// HasHookWithCommand reports whether the "hooks" section of obj, when
// serialised to compact JSON, contains substr. Mirrors Python's
// substr in json.dumps(hooks).
func HasHookWithCommand(obj *orderedmap.OrderedMap, substr string) bool {
	hv, ok := obj.Get("hooks")
	if !ok {
		return false
	}
	hm, ok := asMap(hv)
	if !ok {
		return false
	}
	return strings.Contains(marshalCompact(hm), substr)
}

// RemoveHooksByCommand removes all hook entries whose compact JSON contains
// substr. Empty event arrays are deleted; if the "hooks" map becomes empty it
// is removed from obj entirely.
func RemoveHooksByCommand(obj *orderedmap.OrderedMap, substr string) {
	hv, ok := obj.Get("hooks")
	if !ok {
		return
	}
	hm, ok := asMap(hv)
	if !ok {
		return
	}

	// Snapshot the keys: orderedmap.Keys() returns the LIVE internal slice and
	// Delete() reslices it, so ranging over Keys() while deleting would skip the
	// element after each deletion (e.g. leaving one keld hook behind when several
	// events are all-keld). Copy first, then mutate.
	events := append([]string(nil), hm.Keys()...)
	for _, event := range events {
		av, _ := hm.Get(event)
		arr, ok := av.([]interface{})
		if !ok {
			continue
		}
		var filtered []any
		for _, e := range arr {
			if !strings.Contains(marshalCompact(e), substr) {
				filtered = append(filtered, e)
			}
		}
		if len(filtered) == 0 {
			hm.Delete(event)
		} else {
			hm.Set(event, filtered)
		}
	}

	if len(hm.Keys()) == 0 {
		obj.Delete("hooks")
	} else {
		obj.Set("hooks", hm)
	}
}
