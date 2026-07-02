package pdbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"pdbbot/internal/token"
)

// Channel is a PDB chat channel (group or DM).
type Channel struct {
	ID          string
	Type        string // "group_chat" or "friend_channel"
	Name        string // group display name; empty for DMs
	GroupChatID string // numeric group chat ID, populated for group channels
}

func (c Channel) IsGroup() bool { return c.Type == "group_chat" }

// Message is a single chat message.
type Message struct {
	ID               string
	ChannelID        string
	SenderID         string
	SenderName       string
	Text             string
	CreatedAt        time.Time
	ReplyToID        string // non-empty if this is a reply to another message
	ReplyToSenderID  string // sender of the replied-to message (from wire data directly)
}

// Client wraps token.Manager with PDB chat API methods.
type Client struct {
	mgr *token.Manager
}

func New(mgr *token.Manager) *Client { return &Client{mgr: mgr} }

// ListChannels returns all channels ordered by last activity (most recent first).
// Caller can filter with IsGroup().
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	body := []byte(`{"cursor":"","limit":20,"sort":"friends"}`)
	req, err := token.NewAPIRequest(ctx, "POST", "/im/channels/list", body)
	if err != nil {
		return nil, err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("channels/list: status %d: %s", resp.StatusCode, raw)
	}
	return ParseChannelsJSON(resp.Body)
}

// ParseChannelsJSON parses a channels/list response body. Exported for testing.
func ParseChannelsJSON(r io.Reader) ([]Channel, error) {
	var out struct {
		Data struct {
			Results []struct {
				Channel struct {
					ID          string `json:"id"`
					ChannelType string `json:"channelType"`
					ExtraData   struct {
						Name        string `json:"name"`
						GroupChatID string `json:"groupChatID"`
					} `json:"extraData"`
				} `json:"channel"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("channels/list decode: %w", err)
	}
	channels := make([]Channel, 0, len(out.Data.Results))
	for _, r := range out.Data.Results {
		ch := r.Channel
		channels = append(channels, Channel{
			ID:          ch.ID,
			Type:        ch.ChannelType,
			Name:        strings.TrimSpace(ch.ExtraData.Name),
			GroupChatID: ch.ExtraData.GroupChatID,
		})
	}
	return channels, nil
}

// ListMessages returns messages in the channel newer than sinceID (exclusive),
// in chronological order (oldest first). Pass sinceID="" to get the latest limit.
func (c *Client) ListMessages(ctx context.Context, channelID, sinceID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	path := fmt.Sprintf("/im/messages/list?channelID=%s&cursor=&limit=%d", channelID, limit)
	req, err := token.NewAPIRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("messages/list: status %d: %s", resp.StatusCode, raw)
	}
	return ParseMessagesJSON(resp.Body, sinceID)
}

// ParseMessagesJSON parses a messages/list response, filters to messages newer
// than sinceID, and returns them in chronological order. Exported for testing.
func ParseMessagesJSON(r io.Reader, sinceID string) ([]Message, error) {
	var out struct {
		Data struct {
			Results []wireMessage `json:"results"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("messages/list decode: %w", err)
	}

	// API returns newest-first; reverse to chronological.
	raw := out.Data.Results
	msgs := make([]Message, 0, len(raw))
	for i := len(raw) - 1; i >= 0; i-- {
		msgs = append(msgs, wireToMessage(raw[i]))
	}

	// Filter to messages newer than sinceID.
	if sinceID != "" {
		sinceN, err := strconv.ParseInt(sinceID, 10, 64)
		if err == nil {
			filtered := msgs[:0]
			for _, m := range msgs {
				if n, parseErr := strconv.ParseInt(m.ID, 10, 64); parseErr == nil && n > sinceN {
					filtered = append(filtered, m)
				}
			}
			msgs = filtered
		}
	}

	sort.Slice(msgs, func(i, j int) bool {
		ni, _ := strconv.ParseInt(msgs[i].ID, 10, 64)
		nj, _ := strconv.ParseInt(msgs[j].ID, 10, 64)
		return ni < nj
	})

	return msgs, nil
}

// SendMessage posts a message to channelID. If replyToID is non-empty, the
// message is sent as a thread reply to that message ID.
func (c *Client) SendMessage(ctx context.Context, channelID, text, replyToID string) (string, error) {
	localID := "local_" + uuid.New().String()
	body := map[string]any{
		"channelID": channelID,
		"extraData": map[string]string{"local_id": localID},
		"text":      text,
	}
	if replyToID != "" {
		body["replyTo"] = replyToID
	}
	payload, _ := json.Marshal(body)
	req, err := token.NewAPIRequest(ctx, "POST", "/im/messages/create", payload)
	if err != nil {
		return "", err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("messages/create: status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data wireMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("messages/create decode: %w", err)
	}
	return out.Data.ID, nil
}

// IsGroupAdmin returns true if userID has role "admin" or "mod" in the group chat.
func (c *Client) IsGroupAdmin(ctx context.Context, groupChatID, userID string) (bool, error) {
	req, err := token.NewAPIRequest(ctx, "GET", "/group_chats/"+groupChatID, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("group_chats/%s: status %d: %s", groupChatID, resp.StatusCode, raw)
	}
	var out struct {
		Data struct {
			GroupChat struct {
				Members []struct {
					User struct {
						ID string `json:"id"`
					} `json:"user"`
					Role string `json:"role"`
				} `json:"members"`
			} `json:"groupChat"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("group_chats decode: %w", err)
	}
	for _, m := range out.Data.GroupChat.Members {
		if m.User.ID == userID && (m.Role == "admin" || m.Role == "mod") {
			return true, nil
		}
	}
	return false, nil
}

// DeleteMessage removes a message from a group chat. groupChatID is the
// numeric ID (e.g. "52405"), not the channelID string.
func (c *Client) DeleteMessage(ctx context.Context, groupChatID, messageID string) error {
	path := fmt.Sprintf("/group_chats/%s/message?messageID=%s", groupChatID, messageID)
	req, err := token.NewAPIRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete message: status %d", resp.StatusCode)
	}
	return nil
}

// CreateGroupChatResult holds the result of a group chat creation.
type CreateGroupChatResult struct {
	GroupChatID string // numeric ID, e.g. "52418"
	ChannelID   string // e.g. "group-4885554-63507162"
	Name        string
}

// CreateGroupChat creates a new public group chat with the given name.
// iconToken is the base64 signed image token for the group icon; pass "" to omit.
func (c *Client) CreateGroupChat(ctx context.Context, name, iconToken string) (CreateGroupChatResult, error) {
	body := map[string]string{
		"name":          name,
		"groupChatType": "public",
		"languageID":    "en",
	}
	if iconToken != "" {
		body["icon"] = iconToken
	}
	payload, _ := json.Marshal(body)
	req, err := token.NewAPIRequest(ctx, "POST", "/group_chats", payload)
	if err != nil {
		return CreateGroupChatResult{}, err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return CreateGroupChatResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return CreateGroupChatResult{}, fmt.Errorf("create group_chat: status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data struct {
			GroupChat struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				ChannelID string `json:"channelID"`
			} `json:"groupChat"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CreateGroupChatResult{}, fmt.Errorf("create group_chat decode: %w", err)
	}
	return CreateGroupChatResult{
		GroupChatID: out.Data.GroupChat.ID,
		ChannelID:   out.Data.GroupChat.ChannelID,
		Name:        out.Data.GroupChat.Name,
	}, nil
}

// CreateChat creates or retrieves the DM/request channel with targetUserID.
// Works for both friends and non-friends (returns a request_channel for strangers).
// Returns the channel ID to use with SendMessage.
func (c *Client) CreateChat(ctx context.Context, targetUserID string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"targetUserID": targetUserID})
	req, err := token.NewAPIRequest(ctx, "POST", "/chats/create", payload)
	if err != nil {
		return "", err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("chats/create: status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data struct {
			Channel struct {
				ID string `json:"id"`
			} `json:"channel"`
			ChannelID string `json:"channelID"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("chats/create decode: %w", err)
	}
	if out.Data.Channel.ID != "" {
		return out.Data.Channel.ID, nil
	}
	if out.Data.ChannelID != "" {
		return out.Data.ChannelID, nil
	}
	return "", fmt.Errorf("chats/create: no channel ID in response")
}

// StartTyping sends a typing-started event.
func (c *Client) StartTyping(ctx context.Context, channelID string) error {
	return c.typingEvent(ctx, "/im/events/start_typing", channelID)
}

// EndTyping sends a typing-ended event.
func (c *Client) EndTyping(ctx context.Context, channelID string) error {
	return c.typingEvent(ctx, "/im/events/end_typing", channelID)
}

func (c *Client) typingEvent(ctx context.Context, path, channelID string) error {
	payload, _ := json.Marshal(map[string]string{"channelID": channelID})
	req, err := token.NewAPIRequest(ctx, "POST", path, payload)
	if err != nil {
		return err
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// --- wire types (match the live API response exactly) ---

type wireMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channelID"`
	Creator   struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"creator"`
	Text       string       `json:"text"`
	CreateDate int64        `json:"createDate"` // unix ms
	ReplyTo    *wireMessage `json:"replyTo"`
}

func wireToMessage(w wireMessage) Message {
	m := Message{
		ID:         w.ID,
		ChannelID:  w.ChannelID,
		SenderID:   w.Creator.ID,
		SenderName: w.Creator.Username,
		Text:       w.Text,
		CreatedAt:  time.UnixMilli(w.CreateDate),
	}
	if w.ReplyTo != nil {
		m.ReplyToID = w.ReplyTo.ID
		m.ReplyToSenderID = w.ReplyTo.Creator.ID
	}
	return m
}
