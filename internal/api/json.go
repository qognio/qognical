package api

import "encoding/json"

// Local alias so api.go can call jsonDecode without importing encoding/json
// directly (keeps the import list of the bigger file focused).
func jsonDecode(data []byte, out any) error {
	return json.Unmarshal(data, out)
}
