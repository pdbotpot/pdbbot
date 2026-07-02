package akane

import (
	"math/rand"
	"testing"
	"time"
)

func TestReactionDelayInRange(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for i := range 1000 {
		d := ReactionDelay(rng)
		if d <= 0 {
			t.Errorf("sample %d: delay <= 0: %v", i, d)
		}
		if d > reactionMax+time.Second {
			t.Errorf("sample %d: delay exceeds cap: %v", i, d)
		}
	}
}

func TestTypingDurationInRange(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	cases := []int{0, 10, 50, 120, 500}
	for _, n := range cases {
		for i := range 100 {
			d := TypingDuration(n, rng)
			if d < minTypingDur {
				t.Errorf("n=%d sample %d: %v < floor %v", n, i, d, minTypingDur)
			}
			if d > maxTypingDur {
				t.Errorf("n=%d sample %d: %v > cap %v", n, i, d, maxTypingDur)
			}
		}
	}
}

func TestTypingDurationScalesWithLength(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	short := TypingDuration(5, rng)
	rng2 := rand.New(rand.NewSource(1))
	long := TypingDuration(100, rng2)
	if long <= short {
		t.Errorf("longer text should take longer: short=%v long=%v", short, long)
	}
}

func TestReactionDelayDistribution(t *testing.T) {
	// At least 80% of samples should be < 15s (lognormal is right-skewed but bounded).
	rng := rand.New(rand.NewSource(7))
	under15 := 0
	const N = 1000
	for range N {
		if ReactionDelay(rng) < 15*time.Second {
			under15++
		}
	}
	if under15 < N*80/100 {
		t.Errorf("only %d/%d samples under 15s (want >=80%%)", under15, N)
	}
}
