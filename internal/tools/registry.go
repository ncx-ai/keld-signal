package tools

import (
	"strings"

	"github.com/ncx-ai/keld-cli/internal/errs"
)

// All returns all available adapters in order: Claude, Codex, Gemini.
func All() []Adapter {
	return []Adapter{&ClaudeAdapter{}, &CodexAdapter{}, &GeminiAdapter{}}
}

// Get returns the adapter whose Name() matches the given name.
// If no adapter is found, returns an error with the exact format from registry.py.
func Get(name string) (Adapter, error) {
	for _, adapter := range All() {
		if adapter.Name() == name {
			return adapter, nil
		}
	}
	known := strings.Join(adapterNames(), ", ")
	return nil, errs.New("unknown tool '%s'. Known tools: %s", name, known)
}

// Select returns adapters by name if names is non-empty; otherwise returns all
// adapters where Detect() returns true.
func Select(names []string) ([]Adapter, error) {
	if len(names) > 0 {
		result := make([]Adapter, len(names))
		for i, name := range names {
			adapter, err := Get(name)
			if err != nil {
				return nil, err
			}
			result[i] = adapter
		}
		return result, nil
	}
	var result []Adapter
	for _, adapter := range All() {
		if adapter.Detect() {
			result = append(result, adapter)
		}
	}
	return result, nil
}

// adapterNames returns comma-separated adapter names in order.
func adapterNames() []string {
	adapters := All()
	names := make([]string, len(adapters))
	for i, a := range adapters {
		names[i] = a.Name()
	}
	return names
}
