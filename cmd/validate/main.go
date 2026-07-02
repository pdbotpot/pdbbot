// Validates token lifecycle against the live PDB API.
// Run: go run ./cmd/validate/ state.json
// Writes updated state.json on success.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"pdbbot/internal/token"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: validate <state.json>")
		os.Exit(1)
	}
	statePath := os.Args[1]

	m, err := token.Load(statePath, nil)
	if err != nil {
		fatalf("load: %v", err)
	}
	defer m.Close()

	ctx := context.Background()

	fmt.Println("[1] Refreshing (empty access token should trigger refresh)...")
	tok, err := m.AccessToken(ctx)
	if err != nil {
		fatalf("AccessToken: %v", err)
	}
	fmt.Printf("[1] OK — access token: %s...%s\n", tok[:20], tok[len(tok)-8:])

	fmt.Println("[2] Authenticated API call: GET /users/4885554/irl_preview ...")
	req, _ := token.NewAPIRequest(ctx, "GET", "/users/4885554/irl_preview", nil)
	resp, err := m.Do(ctx, req)
	if err != nil {
		fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fatalf("[2] unexpected status %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	json.Unmarshal(body, &out)
	fmt.Printf("[2] OK — status 200, error field: %v\n", extractErrorCode(out))

	fmt.Println("\nAll checks passed. state.json updated with fresh tokens.")
}

func extractErrorCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return "?"
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
