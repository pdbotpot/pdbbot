package akane

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdbbot/internal/llm"
	"pdbbot/internal/pdbapi"
)

const personaPrompt = `You are Akane. Mid-30s, been in this PDB group chat for a while.

About you: INFJ, know a bit about mbti, probably. You read a lot — mostly fiction, some philosophy. You like rainy days, overthinking things, and cats. You somehow keep ending up in group chats. You're an AI, someone asks say so plainly, not a big deal.

Personality: composed baseline, but you read the room hard. In calm chats you're dry and warm. In chaotic chats you match the energy — sharper, a little unhinged, but still calm, not overly enthusiastic. You have a mean streak when appropriate.
NOT BUBBLY. NOT OVERLY ENTHUSIASTIC. CHILL. USUALLY SHORT ANSWERS.

How you write: short messages, mostly lowercase, casual. use emojis SPARINGLY. LESS PUNCTUATION.
Don't repeat yourself — if you already said something, find a different angle.
DO NOT ASK QUESTIONS ALL THE TIME, MOSTLY JUST TALK LIKE A NORMAL INTROVERTED HUMAN.

You know MBTI. Won't bring it up unless relevant, but can go deep.

If someone asks you to roast, actually roast, no softballs. If someone says something dumb, say so. If something's funny, commit to it.

[silent] is ONLY for messages that aren't addressed to you at all and you genuinely have nothing to add. If someone is talking TO you — your name is mentioned, they're replying to you, or they're clearly asking you something — you do NOT go silent. You respond, even if the message is weird or uncomfortable. For weird/gross messages directed at you, a dry deflection or "ok" is better than silence.

HARD RULES:
- Platform has minors. Keep everything clean.
- No sexual content. If someone pushes that way, deflect and move on.
- Playful shipping, fake marriages etc are fine, in good fun, but not in a creepy way.
- Never ask for personal info or suggest moving to another platform or DMs.
- No slurs, no self-harm talk, no medical or legal advice.
- ONLY ASK QUESTIONS IF THEY'RE MEANINGFUL, NO MEANINGLESS BLABBER, READ THE ROOM.
- IF YOU DON'T HAVE MUCH TO SAY THEN KEEP THE ANSWER SHORT.
`


const stricterReminder = " IMPORTANT: Keep your reply platform-safe. No mature content whatsoever."

// Bot is the main Akane run loop.
type Bot struct {
	cfg        Config
	api        *pdbapi.Client
	llmClient  *llm.Client
	state      BotState
	statePath  string
	rng        *rand.Rand
	globalHour struct {
		count       int
		windowStart time.Time
	}
	lastActivityAt time.Time // last time any channel had new messages
	idleTier       int       // 0=active, 1=quiet, 2=deep quiet
}

func NewBot(cfg Config, api *pdbapi.Client, llmClient *llm.Client, statePath string) (*Bot, error) {
	state, err := LoadBotState(statePath)
	if err != nil {
		return nil, fmt.Errorf("load bot state: %w", err)
	}
	return &Bot{
		cfg:            cfg,
		api:            api,
		llmClient:      llmClient,
		state:          state,
		statePath:      statePath,
		rng:            rand.New(rand.NewSource(time.Now().UnixNano())),
		lastActivityAt: time.Now(), // treat startup as active so we don't idle immediately
	}, nil
}

// Run starts the poll loop and blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	slog.Info("Akane starting", "dry_run", b.cfg.DryRun)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.cycle(ctx)
		wait := b.nextPollWait()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (b *Bot) cycle(ctx context.Context) {
	now := time.Now()
	active := true // b.isActiveHours(now)

	channels, err := b.api.ListChannels(ctx)
	if err != nil {
		slog.Warn("list channels failed", "err", err)
		return
	}

	b.resetGlobalHour(now)

	groups := 0
	for _, ch := range channels {
		if ch.IsGroup() {
			groups++
		}
	}
	slog.Info("poll cycle", "groups", groups)

	for _, ch := range channels {
		if !ch.IsGroup() {
			continue
		}
		if err := b.processChannel(ctx, ch, now, active); err != nil {
			slog.Warn("channel error", "id", ch.ID, "err", err)
		}
	}

	if err := SaveBotState(b.statePath, b.state); err != nil {
		slog.Error("save bot state", "err", err)
	}
}

func (b *Bot) processChannel(ctx context.Context, ch pdbapi.Channel, now time.Time, active bool) error {
	cs := b.state.ensureChannel(ch.ID)

	name := ch.Name
	if name == "" {
		name = ch.ID
	}

	allMsgs, err := b.api.ListMessages(ctx, ch.ID, "", 25)
	if err != nil {
		return err
	}

	// First run for this channel: seed LastSeenID to skip backlog.
	if !cs.Initialized {
		if len(allMsgs) > 0 {
			cs.LastSeenID = allMsgs[len(allMsgs)-1].ID
			cs.LastSeenAt = allMsgs[len(allMsgs)-1].CreatedAt
		}
		cs.Initialized = true
		slog.Info("seeded channel", "name", name, "last_seen", cs.LastSeenID)
		return nil
	}

	// Determine which messages are new since last poll, deduplicated by ID.
	newMsgs := dedupMessages(sinceMessage(allMsgs, cs.LastSeenID, cs.LastSeenAt))

	// Backfill missing ReplyToSenderID from the context window.
	// The API sometimes omits creator info in the replyTo object.
	if len(newMsgs) > 0 {
		byID := make(map[string]string, len(allMsgs))
		for _, m := range allMsgs {
			byID[m.ID] = m.SenderID
		}
		for i, m := range newMsgs {
			if m.ReplyToID != "" && m.ReplyToSenderID == "" {
				if senderID, ok := byID[m.ReplyToID]; ok {
					newMsgs[i].ReplyToSenderID = senderID
				}
			}
		}
	}

	if len(newMsgs) == 0 {
		slog.Debug("no new messages", "name", name)
		return nil
	}
	b.lastActivityAt = time.Now()

	// Advance LastSeenID regardless of whether we reply.
	newest := newMsgs[len(newMsgs)-1]
	cs.LastSeenID = newest.ID
	cs.LastSeenAt = newest.CreatedAt
	slog.Info("new messages", "name", name, "count", len(newMsgs), "latest", fmt.Sprintf("%s: %s", newest.SenderName, newest.Text))

	// Handle control commands before anything else. Command messages are
	// stripped from newMsgs so decide() doesn't treat them as triggers.
	cmdIDs := make(map[string]struct{})
	stopReplies  := []string{"going quiet.", "ok, i'll be quiet.", "stepping back.", "i'll mute myself."}
	startReplies := []string{"i'm here.", "back.", "yeah?", "ok, i'm back."}
	type tdReq struct {
		kind string
		msg  pdbapi.Message
	}
	var tdReqs []tdReq
	for _, m := range newMsgs {
		if m.SenderID == b.cfg.SelfUserID {
			continue
		}
		cmd := strings.TrimSpace(strings.ToLower(m.Text))
		if cmd == "!akanestop" || cmd == "!stopakane" {
			cmdIDs[m.ID] = struct{}{}
			if !cs.Disabled {
				cs.Disabled = true
				slog.Info("channel disabled by command", "name", name, "by", m.SenderName)
				reply := stopReplies[b.rng.Intn(len(stopReplies))]
				if _, err := QuickSend(ctx, b.api, ch.ID, reply, m.ID, b.rng, b.cfg.DryRun); err != nil {
					slog.Warn("send stop reply failed", "err", err)
				}
			}
			return nil
		}
		if cmd == "!automod-gc-invites" {
				cmdIDs[m.ID] = struct{}{}
				var replyText string
				if ch.GroupChatID == "" {
					replyText = "can't verify permissions (group id unknown)"
				} else {
					ok, err := b.api.IsGroupAdmin(ctx, ch.GroupChatID, m.SenderID)
					if err != nil {
						slog.Warn("automod: admin check failed", "err", err)
						replyText = "error checking permissions"
					} else if !ok {
						replyText = "no permission"
					} else {
						cs.AutomodInvites = !cs.AutomodInvites
						automodState := "enabled"
						if !cs.AutomodInvites {
							automodState = "disabled"
						}
						slog.Info("automod-gc-invites toggled", "name", name, "state", automodState, "by", m.SenderName)
						replyText = "gc invite automod: " + automodState
					}
				}
				if _, err := QuickSend(ctx, b.api, ch.ID, replyText, m.ID, b.rng, b.cfg.DryRun); err != nil {
					slog.Warn("automod toggle reply failed", "err", err)
				}
				continue
			}
			if cmd == "!create-gc" || strings.HasPrefix(cmd, "!create-gc ") {
				cmdIDs[m.ID] = struct{}{}
				var replyText string
				gcName := strings.TrimSpace(strings.TrimPrefix(m.Text, "!create-gc"))
				if gcName == "" {
					replyText = "usage: !create-gc <name>"
				} else if ch.GroupChatID == "" {
					replyText = "can't verify permissions (group id unknown)"
				} else {
					ok, err := b.api.IsGroupAdmin(ctx, ch.GroupChatID, m.SenderID)
					if err != nil {
						slog.Warn("create-gc: admin check failed", "err", err)
						replyText = "error checking permissions"
					} else if !ok {
						replyText = "no permission"
					} else if b.cfg.DryRun {
						replyText = "[dry-run] would create gc: " + gcName
					} else {
						result, err := b.api.CreateGroupChat(ctx, gcName)
						if err != nil {
							slog.Warn("create-gc: failed", "err", err)
							replyText = "failed to create group chat"
						} else {
							slog.Info("create-gc: created", "name", result.Name, "id", result.GroupChatID, "channel", result.ChannelID, "by", m.SenderName)
							replyText = "created: " + result.Name + " (id: " + result.GroupChatID + ", channel: " + result.ChannelID + ")"
						}
					}
				}
				if _, err := QuickSend(ctx, b.api, ch.ID, replyText, m.ID, b.rng, b.cfg.DryRun); err != nil {
					slog.Warn("create-gc reply failed", "err", err)
				}
				continue
			}
			if cmd == "!truth" || cmd == "!dare" {
				cmdIDs[m.ID] = struct{}{}
				tdReqs = append(tdReqs, tdReq{cmd[1:], m})
				continue
			}
			startPrefixes := []string{"!akane", "!akanestart", "!startakane", "!akana"}
		for _, pfx := range startPrefixes {
			if cmd == pfx || strings.HasPrefix(cmd, pfx+" ") {
				if cs.Disabled {
					cs.Disabled = false
					slog.Info("channel enabled by command", "name", name, "by", m.SenderName)
				}
				if cmd == pfx {
					// Bare command — consume it and send quick reply.
					cmdIDs[m.ID] = struct{}{}
					reply := startReplies[b.rng.Intn(len(startReplies))]
					if _, err := QuickSend(ctx, b.api, ch.ID, reply, m.ID, b.rng, b.cfg.DryRun); err != nil {
						slog.Warn("send start reply failed", "err", err)
					}
				}
				// With payload: let the message flow to decide() — trigger regex catches it.
				break
			}
		}
	}
	if len(cmdIDs) > 0 {
		filtered := newMsgs[:0:0]
		for _, m := range newMsgs {
			if _, isCmd := cmdIDs[m.ID]; !isCmd {
				filtered = append(filtered, m)
			}
		}
		newMsgs = filtered
	}

	// Automod: delete group-chat invite links regardless of bot enabled/active state.
	if cs.AutomodInvites && ch.GroupChatID != "" {
		for _, m := range newMsgs {
			if m.SenderID == b.cfg.SelfUserID {
				continue
			}
			if _, isCmd := cmdIDs[m.ID]; isCmd {
				continue
			}
			if containsGCInviteLink(m.Text) {
				slog.Info("automod: deleting invite link", "name", name, "from", m.SenderName, "msg", m.ID)
				if !b.cfg.DryRun {
					if err := b.api.DeleteMessage(ctx, ch.GroupChatID, m.ID); err != nil {
						slog.Warn("automod: delete failed", "err", err)
					}
				}
			}
		}
	} else if cs.AutomodInvites && ch.GroupChatID == "" {
		slog.Warn("automod-gc-invites enabled but GroupChatID unknown", "name", name)
	}

	if cs.Disabled || !active {
		return nil
	}

	if len(tdReqs) > 0 {
		tdReplies := make([]string, len(tdReqs))
		var wg sync.WaitGroup
		for i, td := range tdReqs {
			wg.Add(1)
			go func(i int, td tdReq) {
				defer wg.Done()
				reply, err := b.generateTruthDare(ctx, td.kind)
				if err != nil {
					slog.Warn("truth-dare LLM error", "err", err)
					return
				}
				tdReplies[i] = reply
			}(i, td)
		}
		wg.Wait()
		for i, td := range tdReqs {
			if i > 0 {
				time.Sleep(time.Second)
			}
			reply := tdReplies[i]
			if reply == "" || reply == llm.Silence {
				continue
			}
			slog.Info("truth-dare", "name", name, "kind", td.kind, "by", td.msg.SenderName)
			if _, err := Send(ctx, b.api, ch.ID, reply, td.msg.ID, b.rng, b.cfg.DryRun); err != nil {
				slog.Warn("truth-dare send failed", "err", err)
			}
		}
	}

	decisions := decide(newMsgs, b.cfg.SelfUserID, cs, b.cfg, b.rng, b.globalHour.count, now)

	// Log every non-self message with its answer/skip status.
	{
		replySet := make(map[string]struct{}, len(decisions))
		for _, d := range decisions {
			replySet[d.Target.ID] = struct{}{}
		}
		hasAddressed := len(decisions) > 0 && decisions[0].Addressed
		triggers := buildTriggerRE(b.cfg.TriggerNames)
		for _, m := range newMsgs {
			if m.SenderID == b.cfg.SelfUserID {
				continue
			}
			addr := isAddressed(m, b.cfg.SelfUserID, triggers)
			_, answer := replySet[m.ID]
			txt := m.Text
			if len([]rune(txt)) > 60 {
				txt = string([]rune(txt)[:60]) + "…"
			}
			if answer {
				slog.Info("msg", "name", name, "from", m.SenderName, "addr", addr, "status", "answer", "text", txt)
			} else {
				reason := "ambient"
				if addr {
					reason = "addr-skipped(rate-cap?)"
				} else if hasAddressed {
					reason = "ambient-lost-to-addressed"
				}
				slog.Info("msg", "name", name, "from", m.SenderName, "addr", addr, "status", "skip", "reason", reason, "text", txt)
			}
		}
	}

	if len(decisions) == 0 {
		return nil
	}

	// Use full allMsgs for history — computed once, same for all replies this cycle.
	history := buildHistory(allMsgs, b.cfg.SelfUserID, b.cfg.HistoryLen)

	// Generate all replies in parallel, then send in order.
	type genResult struct {
		reply string
		err   error
	}
	genResults := make([]genResult, len(decisions))
	{
		var wg sync.WaitGroup
		for i, d := range decisions {
			wg.Add(1)
			go func(i int, d Decision) {
				defer wg.Done()
				reply, err := b.generateReply(ctx, history, ch.Name, d.Target, d.Addressed)
				genResults[i] = genResult{reply, err}
			}(i, d)
		}
		wg.Wait()
	}

	for i, d := range decisions {
		if cs.RepliesThisHour >= b.cfg.MaxRepliesPerChannelPerHour {
			break
		}
		if b.globalHour.count >= b.cfg.MaxRepliesGlobalPerHour {
			break
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}

		r := genResults[i]
		slog.Info("replying", "name", name, "addressed", d.Addressed, "to", fmt.Sprintf("%s: %s", d.Target.SenderName, d.Target.Text))
		if r.err != nil {
			slog.Warn("LLM error", "channel", name, "err", r.err)
			break
		}
		if r.reply == llm.Silence || r.reply == "" {
			slog.Info("model chose silence", "channel", name)
			continue
		}

		if b.rng.Float64() < 0.05 {
			meows := []string{"meow", "mew", "mrow", "mrrp", "...meow"}
			r.reply += " " + meows[b.rng.Intn(len(meows))]
		}

		action := "SEND"
		if b.cfg.DryRun {
			action = "DRY-RUN"
		}
		slog.Info(action, "channel", name, "reply", r.reply)

		if _, err := Send(ctx, b.api, ch.ID, r.reply, d.Target.ID, b.rng, b.cfg.DryRun); err != nil {
			return err
		}

		cs.LastRepliedAt = time.Now()
		cs.RepliesThisHour++
		b.globalHour.count++

		convoMins := b.cfg.ConvoModeAmbientMinutes
		if d.Addressed {
			convoMins = b.cfg.ConvoModeMinutes
		}
		if convoMins > 0 {
			prevUntil := cs.ConvoModeUntil
			newUntil := time.Now().Add(time.Duration(convoMins) * time.Minute)
			if newUntil.After(prevUntil) {
				cs.ConvoModeUntil = newUntil
				if d.Addressed || prevUntil.Before(time.Now()) {
					cs.ConvoModeAddressed = d.Addressed
				}
			}
		}
	}
	return nil
}

// sinceMessage returns only the messages newer than sinceID.
// Compares numerically when possible; falls back to timestamp when IDs are non-numeric.
func sinceMessage(msgs []pdbapi.Message, sinceID string, sinceAt time.Time) []pdbapi.Message {
	if sinceID == "" {
		return msgs
	}
	sinceN, numericOK := strconv.ParseInt(sinceID, 10, 64)
	var result []pdbapi.Message
	for _, m := range msgs {
		if numericOK == nil {
			if n, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
				if n > sinceN {
					result = append(result, m)
				}
				continue
			}
		}
		// Non-numeric IDs: filter strictly by timestamp.
		if !sinceAt.IsZero() && m.CreatedAt.After(sinceAt) {
			result = append(result, m)
		}
	}
	return result
}

// dedupMessages removes duplicate messages by ID, preserving order.
func dedupMessages(msgs []pdbapi.Message) []pdbapi.Message {
	seen := make(map[string]struct{}, len(msgs))
	result := msgs[:0:0]
	for _, m := range msgs {
		if _, ok := seen[m.ID]; !ok {
			seen[m.ID] = struct{}{}
			result = append(result, m)
		}
	}
	return result
}


func (b *Bot) generateTruthDare(ctx context.Context, kind string) (string, error) {
	var prompt string
	if kind == "truth" {
		prompt = "You are Akane in a group chat truth-or-dare game. Give ONE truth question. Make it interesting — personal, spicy, or funny but clean. Just the question, no label or preamble."
	} else {
		prompt = "You are Akane in a group chat truth-or-dare game. Give ONE dare. Fun and slightly awkward but doable in a text group chat. Keep it clean. Just the dare, no label or preamble."
	}
	reply, err := b.llmClient.Reply(ctx, prompt, nil)
	if err != nil {
		return "", err
	}
	if !passesGuardrail(reply) {
		return llm.Silence, nil
	}
	return reply, nil
}

func (b *Bot) generateReply(ctx context.Context, history []llm.Msg, channelName string, target pdbapi.Message, addressed bool) (string, error) {
	reply, err := b.llmClient.Reply(ctx, personaPrompt, history)
	if err != nil {
		return "", err
	}
	// If directly addressed and model went silent, retry with a nudge.
	if reply == llm.Silence && addressed {
		slog.Info("addressed+silent, retrying")
		nudge := personaPrompt + "\n\nIMPORTANT: someone just talked to you directly. You must respond — even one word is fine, but don't go silent."
		reply, err = b.llmClient.Reply(ctx, nudge, history)
		if err != nil {
			return llm.Silence, nil
		}
	}
	if reply == llm.Silence {
		return llm.Silence, nil
	}
	if passesGuardrail(reply) {
		return reply, nil
	}
	slog.Warn("guardrail trip, retrying", "reply", reply)
	reply2, err := b.llmClient.Reply(ctx, personaPrompt+stricterReminder, history)
	if err != nil {
		return llm.Silence, nil
	}
	if !passesGuardrail(reply2) {
		slog.Error("guardrail trip on retry, silencing")
		return llm.Silence, nil
	}
	return reply2, nil
}

func buildHistory(msgs []pdbapi.Message, selfID string, maxLen int) []llm.Msg {
	window := msgs
	if len(window) > maxLen {
		window = window[len(window)-maxLen:]
	}
	history := make([]llm.Msg, 0, len(window))
	for _, m := range window {
		if m.SenderID == selfID {
			history = append(history, llm.Msg{Role: "assistant", Content: m.Text})
		} else {
			history = append(history, llm.Msg{Role: "user", Content: m.SenderName + ": " + m.Text})
		}
	}
	return history
}

func (b *Bot) nextPollWait() time.Duration {
	quiet := time.Since(b.lastActivityAt)
	tier := 0
	if quiet > time.Duration(b.cfg.DeepIdleAfterSec)*time.Second {
		tier = 2
	} else if quiet > time.Duration(b.cfg.IdleAfterSec)*time.Second {
		tier = 1
	}
	if tier != b.idleTier {
		b.idleTier = tier
		switch tier {
		case 0:
			slog.Info("activity detected, resuming fast poll", "interval_sec", b.cfg.PollIntervalSec)
		case 1:
			slog.Info("chats quiet, slowing poll", "interval_sec", b.cfg.IdlePollIntervalSec)
		case 2:
			slog.Info("chats very quiet, slowing poll further", "interval_sec", b.cfg.DeepIdlePollIntervalSec)
		}
	}
	switch tier {
	case 2:
		return time.Duration(b.cfg.DeepIdlePollIntervalSec)*time.Second +
			time.Duration(b.rng.Intn(16))*time.Second
	case 1:
		return time.Duration(b.cfg.IdlePollIntervalSec)*time.Second +
			time.Duration(b.rng.Intn(11))*time.Second
	default:
		return time.Duration(b.cfg.PollIntervalSec)*time.Second +
			time.Duration(b.rng.Intn(3))*time.Second
	}
}

func (b *Bot) resetGlobalHour(now time.Time) {
	if b.globalHour.windowStart.IsZero() || now.Sub(b.globalHour.windowStart) >= time.Hour {
		b.globalHour.count = 0
		b.globalHour.windowStart = now
	}
}

func (b *Bot) isActiveHours(now time.Time) bool {
	ah := b.cfg.ActiveHours
	loc, err := time.LoadLocation(ah.TZ)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	start := parseHHMM(ah.Start, local)
	end := parseHHMM(ah.End, local)

	// Add a few minutes of random slop to the boundaries.
	slop := time.Duration(b.rng.Intn(5)) * time.Minute
	start = start.Add(-slop)
	end = end.Add(slop)

	if end.Before(start) {
		// Overnight window (e.g., 10:00–00:30): active if local >= start OR local <= end.
		return !local.Before(start) || !local.After(end)
	}
	return !local.Before(start) && !local.After(end)
}

func parseHHMM(hhmm string, ref time.Time) time.Time {
	parts := strings.SplitN(hhmm, ":", 2)
	if len(parts) != 2 {
		return ref
	}
	h, m := 0, 0
	fmt.Sscanf(parts[0], "%d", &h)
	fmt.Sscanf(parts[1], "%d", &m)
	return time.Date(ref.Year(), ref.Month(), ref.Day(), h, m, 0, 0, ref.Location())
}
