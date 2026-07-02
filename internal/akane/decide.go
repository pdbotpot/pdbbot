package akane

import (
	"math/rand"
	"regexp"
	"strings"
	"time"

	"pdbbot/internal/pdbapi"
)

// Decision is the output of the decide function.
type Decision struct {
	Reply     bool
	Addressed bool // true if directly addressed (vs ambient)
	Target    pdbapi.Message
}

// decide returns all messages to reply to this cycle.
// All addressed messages get replies (cooldown bypassed); ambient falls back to at most one.
func decide(
	msgs []pdbapi.Message,
	botID string,
	cs *ChannelState,
	cfg Config,
	rng *rand.Rand,
	globalRepliesThisHour int,
	now time.Time,
) []Decision {
	// Reset hourly counter if window expired.
	if cs.HourWindowStart.IsZero() || now.Sub(cs.HourWindowStart) >= time.Hour {
		cs.RepliesThisHour = 0
		cs.HourWindowStart = now
	}

	// Hard rate caps.
	if cs.RepliesThisHour >= cfg.MaxRepliesPerChannelPerHour {
		return nil
	}
	if globalRepliesThisHour >= cfg.MaxRepliesGlobalPerHour {
		return nil
	}

	// Filter out self-messages.
	human := make([]pdbapi.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.SenderID != botID {
			human = append(human, m)
		}
	}
	if len(human) == 0 {
		return nil
	}

	// Anti-loop: suppress ambient if bot dominates the last 6 messages.
	antiLoop := false
	{
		window := msgs
		if len(window) > 6 {
			window = window[len(window)-6:]
		}
		own := 0
		for _, m := range window {
			if m.SenderID == botID {
				own++
			}
		}
		if own*2 >= len(window) {
			antiLoop = true
		}
	}

	triggers := buildTriggerRE(cfg.TriggerNames)

	// Collect all addressed messages in chronological order — each gets a reply.
	var addressed []Decision
	for _, m := range human {
		if isAddressed(m, botID, triggers) {
			addressed = append(addressed, Decision{Reply: true, Addressed: true, Target: m})
		}
	}
	if len(addressed) > 0 {
		return addressed
	}

	// No addressed messages — try one ambient reply.
	if now.Sub(cs.LastRepliedAt) < time.Duration(cfg.PerChannelCooldownSec)*time.Second {
		return nil
	}
	if antiLoop {
		return nil
	}
	// Pick the most recent non-addressed message.
	for i := len(human) - 1; i >= 0; i-- {
		m := human[i]
		if !isAddressed(m, botID, triggers) {
			prob := cfg.AmbientProb
			if now.Before(cs.ConvoModeUntil) {
				if cs.ConvoModeAddressed {
					prob = 1.0
				} else {
					prob = 0.75
				}
			}
			if rng.Float64() < prob {
				return []Decision{{Reply: true, Addressed: false, Target: m}}
			}
			break
		}
	}
	return nil
}

// isAddressed returns true if the message is directed at the bot.
func isAddressed(m pdbapi.Message, botID string, triggers *regexp.Regexp) bool {
	if triggers != nil && triggers.MatchString(m.Text) {
		return true
	}
	// ReplyToSenderID is populated directly from wire data, so this works
	// even when the original message is outside the current fetch window.
	return m.ReplyToSenderID == botID
}

// buildTriggerRE compiles a word-boundary regexp from the trigger name list.
func buildTriggerRE(names []string) *regexp.Regexp {
	if len(names) == 0 {
		return nil
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = regexp.QuoteMeta(strings.ToLower(n))
	}
	re, err := regexp.Compile(`(?i)\b(` + strings.Join(parts, "|") + `)\b`)
	if err != nil {
		return nil
	}
	return re
}
