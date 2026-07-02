package akane

import (
	"context"
	"math"
	"math/rand"
	"time"

	"pdbbot/internal/pdbapi"
)

const (
	typingSpeedCharsPerSec = 8.0  // chars/s baseline
	typingSpeedJitter      = 2.0  // gaussian stddev chars/s
	minTypingDur           = 300 * time.Millisecond
	maxTypingDur           = 500 * time.Millisecond

	reactionMu    = 0.4 // lognormal mean (ln seconds)
	reactionSigma = 0.4 // lognormal stddev
	reactionMax   = 1 * time.Second
)

// ReactionDelay returns a human-plausible delay before acting on a message.
// Lognormal distribution: mostly a few seconds, occasionally up to ~30s.
func ReactionDelay(rng *rand.Rand) time.Duration {
	raw := math.Exp(reactionMu + reactionSigma*rng.NormFloat64())
	d := time.Duration(raw * float64(time.Second))
	if d > reactionMax {
		d = reactionMax
	}
	return d
}

// TypingDuration returns a plausible typing duration for a message of given length.
func TypingDuration(textLen int, rng *rand.Rand) time.Duration {
	speed := typingSpeedCharsPerSec + rng.NormFloat64()*typingSpeedJitter
	if speed < 3 {
		speed = 3
	}
	d := time.Duration(float64(textLen)/speed*float64(time.Second))
	if d < minTypingDur {
		d = minTypingDur
	}
	if d > maxTypingDur {
		d = maxTypingDur
	}
	return d
}

// QuickSend sends a message with a short fixed delay, used for command acknowledgements.
func QuickSend(ctx context.Context, api *pdbapi.Client, channelID, text, replyToID string, rng *rand.Rand, dryRun bool) (string, error) {
	delay := time.Second + time.Duration(rng.Intn(1500))*time.Millisecond
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(delay):
	}
	if dryRun {
		return "", nil
	}
	return api.SendMessage(ctx, channelID, text, replyToID)
}

// Send performs the humanized send sequence:
//  1. Reaction delay
//  2. StartTyping
//  3. Typing duration
//  4. EndTyping
//  5. SendMessage
//
// If dryRun is true, the actual send is skipped and "(dry-run)" is logged.
// Returns the server-assigned message ID (empty in dry-run).
func Send(
	ctx context.Context,
	api *pdbapi.Client,
	channelID, text, replyToID string,
	rng *rand.Rand,
	dryRun bool,
) (string, error) {
	// 1. Reaction delay.
	react := ReactionDelay(rng)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(react):
	}

	// 2. StartTyping.
	_ = api.StartTyping(ctx, channelID) // best-effort, ignore errors

	// 3. Typing duration.
	typDur := TypingDuration(len(text), rng)
	select {
	case <-ctx.Done():
		_ = api.EndTyping(ctx, channelID)
		return "", ctx.Err()
	case <-time.After(typDur):
	}

	// 4. EndTyping.
	_ = api.EndTyping(ctx, channelID)

	// 5. Send (or dry-run log).
	if dryRun {
		return "", nil
	}
	return api.SendMessage(ctx, channelID, text, replyToID)
}
