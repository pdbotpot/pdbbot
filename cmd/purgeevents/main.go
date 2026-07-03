// One-shot tool: delete all event (non-regular) messages from a named group chat.
// Run: go run ./cmd/purgeevents/ state.json "PDB GROUP CHAT HUB"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"pdbbot/internal/pdbapi"
	"pdbbot/internal/token"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: purgeevents <state.json> <gc-name>")
		os.Exit(1)
	}
	statePath, gcName := os.Args[1], os.Args[2]

	mgr, err := token.Load(statePath, nil)
	if err != nil {
		fatal("load state: %v", err)
	}
	defer mgr.Close()
	ctx := context.Background()
	client := pdbapi.New(mgr)

	channels, err := client.ListChannels(ctx)
	if err != nil {
		fatal("list channels: %v", err)
	}

	var channelID, groupChatID string
	for _, ch := range channels {
		if strings.EqualFold(ch.Name, gcName) {
			channelID = ch.ID
			groupChatID = ch.GroupChatID
			break
		}
	}
	if channelID == "" {
		fatal("channel %q not found", gcName)
	}
	fmt.Printf("found: channelID=%s groupChatID=%s\n", channelID, groupChatID)

	// Fetch up to 50 recent messages including events.
	msgs, err := fetchAllMessages(ctx, mgr, channelID)
	if err != nil {
		fatal("list messages: %v", err)
	}

	deleted := 0
	for _, m := range msgs {
		if m.MessageType == "" || m.MessageType == "regular" {
			continue
		}
		fmt.Printf("deleting event msg id=%s type=%s text=%q\n", m.ID, m.MessageType, m.Text)
		if err := client.DeleteMessage(ctx, groupChatID, m.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN delete %s: %v\n", m.ID, err)
		} else {
			deleted++
		}
	}
	fmt.Printf("done: deleted %d event messages\n", deleted)
}

type rawMsg struct {
	ID          string `json:"id"`
	MessageType string `json:"messageType"`
	Text        string `json:"text"`
}

func fetchAllMessages(ctx context.Context, mgr *token.Manager, channelID string) ([]rawMsg, error) {
	path := fmt.Sprintf("/im/messages/list?channelID=%s&cursor=&limit=50", channelID)
	req, err := token.NewAPIRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mgr.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data struct {
			Results []rawMsg `json:"results"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data.Results, nil
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+f+"\n", a...)
	os.Exit(1)
}
