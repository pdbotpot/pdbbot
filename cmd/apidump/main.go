// Dumps live API responses for channels/list and messages/list to stdout.
// Run: go run ./cmd/apidump/ state.json
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
		fmt.Fprintln(os.Stderr, "usage: apidump <state.json>")
		os.Exit(1)
	}
	m, err := token.Load(os.Args[1], nil)
	if err != nil {
		fatal("load: %v", err)
	}
	defer m.Close()
	ctx := context.Background()

	// 1. channels/list
	fmt.Println("=== POST /im/channels/list ===")
	body := []byte(`{"cursor":"","limit":20,"sort":"friends"}`)
	req, _ := token.NewAPIRequest(ctx, "POST", "/im/channels/list", body)
	dump(ctx, m, req)

	fmt.Println()

	// 2. messages/list for a known group channel (just first 3 messages)
	fmt.Println("=== GET /im/messages/list (group channel) ===")
	req2, _ := token.NewAPIRequest(ctx, "GET",
		"/im/messages/list?channelID=group-2869485-f7516388-9d55-4bf3-8f75-192f67bd9290&cursor=&limit=3", nil)
	dump(ctx, m, req2)

	fmt.Println()

	// 3. DM channel messages
	fmt.Println("=== GET /im/messages/list (DM channel) ===")
	req3, _ := token.NewAPIRequest(ctx, "GET",
		"/im/messages/list?channelID=5979435-488555406920315&cursor=&limit=3", nil)
	dump(ctx, m, req3)
}

func dump(ctx context.Context, m *token.Manager, req *http.Request) {
	resp, err := m.Do(ctx, req)
	if err != nil {
		fatal("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	fmt.Printf("HTTP %d\n", resp.StatusCode)
	var out any
	if json.Unmarshal(raw, &out) == nil {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("[raw %d bytes] %q\n", len(raw), raw[:min(200, len(raw))])
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+f+"\n", a...)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
