package application

import (
	"math"
	"testing"
	"time"
)

func TestReferenceRetryDelayDoublesUpToTheCap(t *testing.T) {
	policy := ReferenceRetryPolicy{BaseDelay: time.Second, MaxDelay: time.Minute}

	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, seconds := range want {
		if got := policy.delayAfter(i + 1); got != seconds*time.Second {
			t.Errorf("delayAfter(%d) = %v, want %v", i+1, got, seconds*time.Second)
		}
	}

	if got := policy.delayAfter(math.MaxInt32); got != time.Minute {
		t.Errorf("delayAfter(huge) = %v, want the cap", got)
	}
	uneven := ReferenceRetryPolicy{BaseDelay: 3 * time.Second, MaxDelay: 10 * time.Second}
	if got := uneven.delayAfter(3); got != 10*time.Second {
		t.Errorf("delayAfter(3) = %v, want the cap when doubling would pass it", got)
	}
}

func TestReferenceRetryIsExhaustedAtMaxAttempts(t *testing.T) {
	policy := ReferenceRetryPolicy{MaxAttempts: 3}
	if policy.exhausted(2) || !policy.exhausted(3) {
		t.Errorf("exhausted(2) = %v, exhausted(3) = %v; want false and true", policy.exhausted(2), policy.exhausted(3))
	}
}

func TestReferenceRetryPolicyValidation(t *testing.T) {
	if err := DefaultReferenceRetryPolicy().Validate(); err != nil {
		t.Fatalf("default policy is invalid: %v", err)
	}

	invalid := map[string]func(*ReferenceRetryPolicy){
		"zero base delay":            func(p *ReferenceRetryPolicy) { p.BaseDelay = 0 },
		"max delay below base delay": func(p *ReferenceRetryPolicy) { p.MaxDelay = p.BaseDelay - 1 },
		"zero attempts":              func(p *ReferenceRetryPolicy) { p.MaxAttempts = 0 },
		"zero lease":                 func(p *ReferenceRetryPolicy) { p.Lease = 0 },
		"zero batch size":            func(p *ReferenceRetryPolicy) { p.BatchSize = 0 },
	}
	for name, change := range invalid {
		policy := DefaultReferenceRetryPolicy()
		change(&policy)
		if err := policy.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
