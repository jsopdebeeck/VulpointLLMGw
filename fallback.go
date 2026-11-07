package main

import (
	"log"
	"slices"
	"strings"
	"sync"
	"time"
)

// Automatic fallback (option, off by default, see FallbackCfg).
//
// A request first goes to the candidates route() found (targets of the profile serving the model),
// then to the fallbacks those targets declare, if the profile allows them. A target that fails
// trip_after times within trip_window_sec is set aside for cooldown_sec: its requests go straight
// to the next candidates, and it moves to the end of the list (still tried if everything else
// fails). Once the cooldown is over, a single request tries it again: a success brings it back.

// withFallbacks appends to cands the fallbacks of its targets, in order, when the profile
// allows them and they are not already candidates.
func withFallbacks(cfg *Config, p *Profile, api string, cands []cand) []cand {
	out := slices.Clone(cands)
	have := map[string]bool{}
	for _, c := range cands {
		have[c.name] = true
	}
	for _, c := range cands {
		for _, f := range c.t.fb {
			t := cfg.Targets[f.target]
			if have[f.target] || t == nil || t.Disabled || t.API != api || !slices.Contains(p.Targets, f.target) {
				continue
			}
			have[f.target] = true
			out = append(out, cand{f.target, t, f.model})
		}
	}
	return out
}

// forcedTarget: the client named the target itself (target/model), which disables the fallback.
func forcedTarget(cfg *Config, model string) bool {
	i := strings.Index(model, "/")
	return i > 0 && cfg.Targets[model[:i]] != nil
}

// breaker: the state of each target, kept in memory (a restart forgets it). Its zero value is ready.
type breaker struct {
	mu sync.Mutex
	st map[string]*brState
}

type brState struct {
	fails   []time.Time // recent failures, within the trip window
	until   time.Time   // set aside until then
	probeAt time.Time   // a trial request is under way (zero: none)
}

// order moves the targets set aside to the end of the list. Once the cooldown is over, the first
// request to come is the trial: the target stays in its place. probeTTL frees a trial whose
// outcome never came back.
func (b *breaker) order(cands []cand, probeTTL time.Duration) (out []cand, aside []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	var later []cand
	for _, c := range cands {
		s := b.st[c.name]
		switch {
		case s == nil || s.until.IsZero():
			out = append(out, c)
		case now.Before(s.until):
			later, aside = append(later, c), append(aside, c.name)
		case s.probeAt.IsZero() || now.Sub(s.probeAt) > probeTTL:
			s.probeAt = now
			out = append(out, c)
		default: // another request is trying it
			later, aside = append(later, c), append(aside, c.name)
		}
	}
	return append(out, later...), aside
}

// result records the outcome of an attempt; it returns true when the target is (again) set aside.
func (b *breaker) result(name string, ok bool, fc FallbackCfg) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.st[name]
	now := time.Now()
	if ok { // a success only matters for a target set aside or under trial
		if s != nil && !s.until.IsZero() {
			log.Printf("repli : cible %s rétablie", name)
			delete(b.st, name)
		}
		return false
	}
	if s == nil {
		if b.st == nil {
			b.st = map[string]*brState{}
		}
		s = &brState{}
		b.st[name] = s
	}
	if !s.probeAt.IsZero() { // the trial failed: set aside again
		s.probeAt, s.until, s.fails = time.Time{}, now.Add(time.Duration(fc.Cooldown)*time.Second), nil
		log.Printf("repli : cible %s toujours en échec, mise de côté %d s", name, fc.Cooldown)
		return true
	}
	if now.Before(s.until) {
		return false
	}
	win := now.Add(-time.Duration(fc.TripWindow) * time.Second)
	s.fails = slices.DeleteFunc(append(s.fails, now), func(t time.Time) bool { return t.Before(win) })
	if len(s.fails) >= fc.TripAfter {
		s.until, s.fails = now.Add(time.Duration(fc.Cooldown)*time.Second), nil
		log.Printf("repli : cible %s mise de côté %d s (%d échecs en %d s)", name, fc.Cooldown, fc.TripAfter, fc.TripWindow)
		return true
	}
	return false
}

// release frees a trial interrupted by the client (no outcome to record).
func (b *breaker) release(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.st[name]; s != nil {
		s.probeAt = time.Time{}
	}
}

// Snapshot: the targets set aside (Unix time of the end of the cooldown) or under trial.
func (b *breaker) Snapshot() map[string]map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]map[string]any{}
	now := time.Now()
	for n, s := range b.st {
		if s.until.IsZero() {
			continue
		}
		out[n] = map[string]any{"until": s.until.Unix(), "aside": now.Before(s.until), "trial": !s.probeAt.IsZero()}
	}
	return out
}
