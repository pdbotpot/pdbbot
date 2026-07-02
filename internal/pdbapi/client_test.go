package pdbapi_test

import (
	"strings"
	"testing"
	"time"

	"pdbbot/internal/pdbapi"
)

// Saved response bodies from live API calls (apidump output, trimmed for testing).

const channelsListJSON = `{
  "data": {
    "limit": 20,
    "nextCursor": "MTc4MjgwNzg0OSwyNzExMjQ1",
    "results": [
      {
        "channel": {
          "archived": false,
          "channelType": "group_chat",
          "createDate": 1781271883000,
          "creator": {"id": "4753315", "username": "Equanimily"},
          "extraData": {
            "groupChatID": "51114",
            "groupChatType": "private",
            "image": "https://example.com/img.jpeg",
            "name": "War of Races "
          },
          "id": "group-4753315-29873019",
          "members": [],
          "permissions": ["subscribe_channel", "send_message"],
          "sort": 1782931453000,
          "unreadMessageCount": 2,
          "updateDate": 1782856575000
        },
        "message": {
          "channelID": "group-4753315-29873019",
          "createDate": 1782931453000,
          "creator": {"id": "4150546", "username": "rexonn"},
          "extraData": {"local_id": "local_db9bc157-c5e3-4f45-9cb7-e85dd0a2648a"},
          "id": "118664204",
          "isActive": true,
          "messageType": "regular",
          "replyTo": null,
          "seq": 1234,
          "text": "Why bald man 🤨",
          "updateDate": 1782931453000
        },
        "userChannelConfig": {"hide": false, "mute": false}
      },
      {
        "channel": {
          "archived": false,
          "channelType": "friend_channel",
          "createDate": 1782640852000,
          "creator": {"id": "5979435", "username": "SoRAaa"},
          "extraData": {"channelType": "friend_channel"},
          "id": "5979435-488555406920315",
          "members": [],
          "permissions": [],
          "sort": 1782931021000,
          "unreadMessageCount": 0,
          "updateDate": 1782931021000
        },
        "message": {
          "channelID": "5979435-488555406920315",
          "createDate": 1782931021000,
          "creator": {"id": "5979435", "username": "SoRAaa"},
          "extraData": {"local_id": "local_xyz"},
          "id": "118638028",
          "isActive": true,
          "messageType": "regular",
          "replyTo": null,
          "seq": 6,
          "text": "cute",
          "updateDate": 1782931021000
        },
        "userChannelConfig": {"hide": false, "mute": false}
      }
    ]
  },
  "error": {"code": "S20000", "message": "OK"}
}`

const messagesListJSON = `{
  "data": {
    "limit": 3,
    "nextCursor": "9326",
    "results": [
      {
        "channelID": "group-2869485-f7516388-9d55-4bf3-8f75-192f67bd9290",
        "createDate": 1782930449000,
        "creator": {"id": "5852633", "username": "True_Mockery"},
        "extraData": {"local_id": "local_66156f45-6f85-48d5-8c24-d551d135f206"},
        "id": "118605824",
        "isActive": true,
        "messageType": "regular",
        "replyTo": null,
        "seq": 9329,
        "text": "what's up",
        "updateDate": 1782930449000
      },
      {
        "channelID": "group-2869485-f7516388-9d55-4bf3-8f75-192f67bd9290",
        "createDate": 1782920000000,
        "creator": {"id": "4885554", "username": "meowiepoops"},
        "extraData": {"local_id": "local_aaaa"},
        "id": "118500000",
        "isActive": true,
        "messageType": "regular",
        "replyTo": null,
        "seq": 9327,
        "text": "hello",
        "updateDate": 1782920000000
      },
      {
        "channelID": "group-2869485-f7516388-9d55-4bf3-8f75-192f67bd9290",
        "createDate": 1782910000000,
        "creator": {"id": "9999999", "username": "someone"},
        "extraData": {"local_id": "local_bbbb"},
        "id": "118400000",
        "isActive": true,
        "messageType": "regular",
        "replyTo": {
          "id": "118300000",
          "channelID": "group-2869485-f7516388-9d55-4bf3-8f75-192f67bd9290",
          "createDate": 1782900000000,
          "creator": {"id": "4885554", "username": "meowiepoops"},
          "text": "replied-to message",
          "extraData": {}
        },
        "seq": 9326,
        "text": "replying to you",
        "updateDate": 1782910000000
      }
    ]
  },
  "error": {"code": "S20000"}
}`

// ParseChannels and ParseMessages are exported for testing.
// They live on the package since client methods call internal parsers —
// here we test the wire-format parsing by calling the exported helpers.

func TestParseChannelsFromWire(t *testing.T) {
	channels, err := pdbapi.ParseChannelsJSON(strings.NewReader(channelsListJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("want 2 channels, got %d", len(channels))
	}

	group := channels[0]
	if group.ID != "group-4753315-29873019" {
		t.Errorf("group ID: got %q", group.ID)
	}
	if !group.IsGroup() {
		t.Error("expected IsGroup() == true")
	}
	if group.Name != "War of Races" {
		t.Errorf("group name: got %q", group.Name)
	}

	dm := channels[1]
	if dm.IsGroup() {
		t.Error("DM channel should not be a group")
	}
	if dm.ID != "5979435-488555406920315" {
		t.Errorf("DM ID: got %q", dm.ID)
	}
}

func TestParseMessagesFromWire(t *testing.T) {
	msgs, err := pdbapi.ParseMessagesJSON(strings.NewReader(messagesListJSON), "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// API returns newest-first; ParseMessages reverses to oldest-first.
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}

	// After reversal, oldest message (id 118400000) should be first.
	oldest := msgs[0]
	if oldest.ID != "118400000" {
		t.Errorf("oldest msg ID: got %q, want 118400000", oldest.ID)
	}
	if oldest.ReplyToID != "118300000" {
		t.Errorf("replyToID: got %q, want 118300000", oldest.ReplyToID)
	}

	newest := msgs[2]
	if newest.ID != "118605824" {
		t.Errorf("newest msg ID: got %q, want 118605824", newest.ID)
	}
	if newest.SenderID != "5852633" {
		t.Errorf("senderID: got %q", newest.SenderID)
	}
	expectedTime := time.UnixMilli(1782930449000)
	if !newest.CreatedAt.Equal(expectedTime) {
		t.Errorf("createdAt: got %v, want %v", newest.CreatedAt, expectedTime)
	}
}

func TestFilterBySinceID(t *testing.T) {
	// sinceID="118400000" → only messages with ID > 118400000 returned
	msgs, err := pdbapi.ParseMessagesJSON(strings.NewReader(messagesListJSON), "118400000")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages after filter, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.ID == "118400000" {
			t.Error("sinceID message should be excluded")
		}
	}
}
