package akane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type LLMConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

type ActiveHours struct {
	TZ    string `json:"tz"`
	Start string `json:"start"` // "HH:MM"
	End   string `json:"end"`   // "HH:MM"
}

type Config struct {
	PollIntervalSec              int         `json:"poll_interval_sec"`
	TriggerNames                 []string    `json:"trigger_names"`
	AmbientProb                  float64     `json:"ambient_prob"`
	PerChannelCooldownSec        int         `json:"per_channel_cooldown_sec"`
	MaxRepliesPerChannelPerHour  int         `json:"max_replies_per_channel_per_hour"`
	MaxRepliesGlobalPerHour      int         `json:"max_replies_global_per_hour"`
	HistoryLen                   int         `json:"history_len"`
	ConvoModeMinutes             int         `json:"convo_mode_minutes"`         // convo window after name/reply-to trigger
	ConvoModeAmbientMinutes      int         `json:"convo_mode_ambient_minutes"` // shorter window after ambient trigger
	IdleAfterSec                 int         `json:"idle_after_sec"`              // seconds quiet → tier-1 slow poll
	IdlePollIntervalSec          int         `json:"idle_poll_interval_sec"`      // tier-1 poll interval
	DeepIdleAfterSec             int         `json:"deep_idle_after_sec"`         // seconds quiet → tier-2 slow poll
	DeepIdlePollIntervalSec      int         `json:"deep_idle_poll_interval_sec"` // tier-2 poll interval
	ActiveHours                  ActiveHours            `json:"active_hours"`
	Providers                    map[string]LLMConfig   `json:"providers"`
	DryRun                       bool                   `json:"dry_run"`
	SelfUserID                   string      `json:"self_user_id"`
	DefaultGroupChatIcon         string      `json:"default_group_chat_icon"`
}

func DefaultConfig() Config {
	return Config{
		PollIntervalSec:             15,
		TriggerNames:                []string{"akane"},
		AmbientProb:                 0.10,
		PerChannelCooldownSec:       2,
		MaxRepliesPerChannelPerHour: 200,
		MaxRepliesGlobalPerHour:     500,
		ConvoModeMinutes:            4,
		ConvoModeAmbientMinutes:     1,
		IdleAfterSec:                300,
		IdlePollIntervalSec:         30,
		DeepIdleAfterSec:            900,
		DeepIdlePollIntervalSec:     90,
		HistoryLen:                  12,
		ActiveHours: ActiveHours{TZ: "Europe/Tallinn", Start: "10:00", End: "00:30"},
		Providers: map[string]LLMConfig{
			"groq": {
				BaseURL: "https://api.groq.com/openai/v1",
				Model:   "openai/gpt-oss-20b",
			},
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Model:   "gpt-4o-mini",
			},
		},
		SelfUserID: "4885554",
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ChannelState tracks per-channel reply counters and the last-seen message ID.
type ChannelState struct {
	Initialized        bool      `json:"initialized"`
	Disabled           bool      `json:"disabled"`
	AutomodInvites     bool      `json:"automod_invites"`
	ModsOnly           bool      `json:"mods_only"`
	AutoDeleteEvents   bool      `json:"auto_delete_events"`
	ModLocked          bool      `json:"mod_locked"` // only a mod/admin can re-enable
	GCLinksOnly        bool      `json:"gc_links_only"`
	NoDuplicates       bool      `json:"no_duplicates"`
	AntiFlood          bool           `json:"anti_flood"`
	AntiFloodMax       int            `json:"anti_flood_max"`                  // 0 means use default (3)
	FloodedTexts       map[string]int `json:"flooded_texts,omitempty"`         // "userID\ttext" → stale poll count
	LastSeenID         string    `json:"last_seen_id"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	LastRepliedAt      time.Time `json:"last_replied_at"`
	RepliesThisHour    int       `json:"replies_this_hour"`
	HourWindowStart    time.Time `json:"hour_window_start"`
	ConvoModeUntil     time.Time `json:"convo_mode_until"`     // elevated reply prob while active
	ConvoModeAddressed bool      `json:"convo_mode_addressed"` // true = entered via name/reply (100%), false = ambient (75%)
}

// BotState is the full persisted state for all channels.
type BotState struct {
	Channels         map[string]*ChannelState `json:"channels"`
	PendingTransfers map[string]string        `json:"pending_transfers,omitempty"` // groupChatID → requesterUserID
}

func NewBotState() BotState {
	return BotState{
		Channels:         make(map[string]*ChannelState),
		PendingTransfers: make(map[string]string),
	}
}

func LoadBotState(path string) (BotState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewBotState(), nil
		}
		return BotState{}, err
	}
	var s BotState
	if err := json.Unmarshal(b, &s); err != nil {
		return BotState{}, err
	}
	if s.Channels == nil {
		s.Channels = make(map[string]*ChannelState)
	}
	if s.PendingTransfers == nil {
		s.PendingTransfers = make(map[string]string)
	}
	return s, nil
}

func SaveBotState(path string, s BotState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".akane-state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return os.Rename(name, path)
}

// ensureChannel returns the ChannelState for id, creating it if absent.
func (s *BotState) ensureChannel(id string) *ChannelState {
	if s.Channels[id] == nil {
		s.Channels[id] = &ChannelState{}
	}
	return s.Channels[id]
}
