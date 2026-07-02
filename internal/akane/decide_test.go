package akane

import (
	"math/rand"
	"testing"
	"time"

	"pdbbot/internal/pdbapi"
)

const botID = "4885554"

func msg(id, senderID, text, replyToID string, t time.Time) pdbapi.Message {
	return pdbapi.Message{
		ID: id, SenderID: senderID, SenderName: "user",
		Text: text, CreatedAt: t, ReplyToID: replyToID,
	}
}

func freshState(now time.Time) *ChannelState {
	return &ChannelState{
		LastRepliedAt:   now.Add(-2 * time.Minute), // well past cooldown
		HourWindowStart: now.Add(-30 * time.Minute),
		RepliesThisHour: 0,
	}
}

func defaultCfg() Config {
	c := DefaultConfig()
	c.SelfUserID = botID
	c.AmbientProb = 1.0 // force ambient to always fire (override for specific tests)
	return c
}

func rng() *rand.Rand { return rand.New(rand.NewSource(42)) }

func TestAddressedByName(t *testing.T) {
	now := time.Now()
	msgs := []pdbapi.Message{
		msg("1", "9999", "hey akane what's your type?", "", now),
	}
	d := decide(msgs, botID, freshState(now), defaultCfg(), rng(), 0, now)
	if !d.Reply || !d.Addressed {
		t.Errorf("expected addressed reply, got %+v", d)
	}
	if d.Target.ID != "1" {
		t.Errorf("wrong target: %q", d.Target.ID)
	}
}

func TestAddressedCaseInsensitive(t *testing.T) {
	now := time.Now()
	msgs := []pdbapi.Message{
		msg("1", "9999", "AKANE what do you think?", "", now),
	}
	d := decide(msgs, botID, freshState(now), defaultCfg(), rng(), 0, now)
	if !d.Reply || !d.Addressed {
		t.Errorf("expected addressed, got %+v", d)
	}
}

func TestAddressedReplyToBot(t *testing.T) {
	now := time.Now()
	// Message 1 is the bot's. Message 2 is a reply to it.
	msgs := []pdbapi.Message{
		msg("1", botID, "hello everyone", "", now.Add(-1*time.Minute)),
		msg("2", "9999", "that's cool", "1", now),
	}
	d := decide(msgs, botID, freshState(now), defaultCfg(), rng(), 0, now)
	if !d.Reply || !d.Addressed {
		t.Errorf("expected addressed reply (reply-to-bot), got %+v", d)
	}
}

func TestAmbientFires(t *testing.T) {
	now := time.Now()
	cfg := defaultCfg()
	cfg.AmbientProb = 1.0
	msgs := []pdbapi.Message{
		msg("1", "9999", "anyone want to chat?", "", now),
	}
	d := decide(msgs, botID, freshState(now), cfg, rng(), 0, now)
	if !d.Reply {
		t.Error("expected ambient reply with prob=1.0")
	}
	if d.Addressed {
		t.Error("should be ambient, not addressed")
	}
}

func TestAmbientSuppressedByProb(t *testing.T) {
	now := time.Now()
	cfg := defaultCfg()
	cfg.AmbientProb = 0.0
	msgs := []pdbapi.Message{
		msg("1", "9999", "general chat", "", now),
	}
	d := decide(msgs, botID, freshState(now), cfg, rng(), 0, now)
	if d.Reply {
		t.Error("ambient should be suppressed with prob=0")
	}
}

func TestCooldownSuppresses(t *testing.T) {
	now := time.Now()
	cs := freshState(now)
	cs.LastRepliedAt = now.Add(-10 * time.Second) // within 45s cooldown
	msgs := []pdbapi.Message{
		msg("1", "9999", "akane!", "", now),
	}
	d := decide(msgs, botID, cs, defaultCfg(), rng(), 0, now)
	if d.Reply {
		t.Error("should be suppressed by cooldown")
	}
}

func TestHourlyCapSuppresses(t *testing.T) {
	now := time.Now()
	cs := freshState(now)
	cs.RepliesThisHour = 8 // at cap
	msgs := []pdbapi.Message{
		msg("1", "9999", "akane!", "", now),
	}
	d := decide(msgs, botID, cs, defaultCfg(), rng(), 0, now)
	if d.Reply {
		t.Error("should be suppressed by hourly cap")
	}
}

func TestGlobalCapSuppresses(t *testing.T) {
	now := time.Now()
	msgs := []pdbapi.Message{
		msg("1", "9999", "akane!", "", now),
	}
	d := decide(msgs, botID, freshState(now), defaultCfg(), rng(), 20, now) // global at cap
	if d.Reply {
		t.Error("should be suppressed by global cap")
	}
}

func TestAntiLoopSuppressesAmbient(t *testing.T) {
	now := time.Now()
	cfg := defaultCfg()
	cfg.AmbientProb = 1.0
	// Last 6 messages: 3 from bot = exactly half → suppress ambient
	msgs := []pdbapi.Message{
		msg("1", botID, "hi", "", now.Add(-5*time.Minute)),
		msg("2", botID, "hm", "", now.Add(-4*time.Minute)),
		msg("3", botID, "hey", "", now.Add(-3*time.Minute)),
		msg("4", "9999", "lol", "", now.Add(-2*time.Minute)),
		msg("5", "9999", "ok", "", now.Add(-1*time.Minute)),
		msg("6", "9999", "general message", "", now),
	}
	d := decide(msgs, botID, freshState(now), cfg, rng(), 0, now)
	if d.Reply && !d.Addressed {
		t.Error("anti-loop should suppress ambient when bot dominates")
	}
}

func TestAntiLoopAllowsAddressed(t *testing.T) {
	now := time.Now()
	cfg := defaultCfg()
	// Anti-loop active (bot has 4/6 messages).
	msgs := []pdbapi.Message{
		msg("1", botID, "hi", "", now.Add(-5*time.Minute)),
		msg("2", botID, "hm", "", now.Add(-4*time.Minute)),
		msg("3", botID, "hey", "", now.Add(-3*time.Minute)),
		msg("4", botID, "yo", "", now.Add(-2*time.Minute)),
		msg("5", "9999", "ok", "", now.Add(-1*time.Minute)),
		msg("6", "9999", "akane what do you think?", "", now),
	}
	d := decide(msgs, botID, freshState(now), cfg, rng(), 0, now)
	if !d.Reply || !d.Addressed {
		t.Error("addressed should still work even under anti-loop")
	}
}

func TestSelfMessagesIgnored(t *testing.T) {
	now := time.Now()
	cfg := defaultCfg()
	cfg.AmbientProb = 1.0
	msgs := []pdbapi.Message{
		msg("1", botID, "I said something", "", now),
	}
	d := decide(msgs, botID, freshState(now), cfg, rng(), 0, now)
	if d.Reply {
		t.Error("should not reply to own messages")
	}
}

func TestHourlyWindowReset(t *testing.T) {
	now := time.Now()
	cs := &ChannelState{
		LastRepliedAt:   now.Add(-2 * time.Minute),
		RepliesThisHour: 8,                           // at cap
		HourWindowStart: now.Add(-61 * time.Minute), // window expired
	}
	msgs := []pdbapi.Message{
		msg("1", "9999", "akane!", "", now),
	}
	d := decide(msgs, botID, cs, defaultCfg(), rng(), 0, now)
	if !d.Reply {
		t.Error("expected reply after hourly window reset")
	}
	if cs.RepliesThisHour != 0 {
		t.Errorf("counter should be reset to 0, got %d", cs.RepliesThisHour)
	}
}
