package core

import (
	"math/rand"
	"slices"

	"github.com/open-spaced-repetition/go-fsrs/v3"
)

// A Session is the queue of cards for one sitting with one deck.
//
// FSRS owns the long-term schedule - the due date, measured in days. It does
// not and cannot own what you see next while you are actually sitting there,
// and the two used to be conflated: the queue was "every card whose Due has
// passed", redrawn at random on every advance (see RandomDueCard). That had
// three consequences.
//
// Rating Again did not bring the card back. Under the long-term scheduler
// every interval, Again included, is clamped to a minimum of one day, so the
// card you had just failed was the one card guaranteed not to reappear. Under
// the short-term scheduler it came back only after five minutes of wall clock
// had passed, which is not a thing to make someone wait for mid-session.
//
// A never-seen card has no Due at all, which reads as "due since the zero
// time", so a freshly generated deck offered all five hundred of its words at
// once with nothing tracking how many were already half-learned.
//
// And drawing at random with replacement meant a card could come up twice in
// a row while another due card was never shown.
//
// A Session fixes all three by deciding the running order once, up front, and
// then walking it.
type Session struct {
	cfg SessionConfig

	// queue holds card keys (see Card.Key), not indices into a slice, so it
	// survives the caller reloading the deck from disk between ratings -
	// which the web UI does on every request.
	queue []string
	pos   int

	// held is the new (never-seen) due cards this deck could still
	// introduce, in the order they would be, minus the ones already let in.
	held []string

	// pending marks new cards already let into this sitting. Membership is
	// permanent even after the card is rated - inFlightNew is what actually
	// tells admitted-and-done-with apart from admitted-and-still-ahead, by
	// checking live State rather than pruning this map.
	pending map[string]bool
}

// SessionConfig is the learning-mode policy. The zero value is a session with
// no new-card gate and no re-queueing, i.e. the old behaviour.
type SessionConfig struct {
	// LearningPool caps how many never-seen cards may be in progress at once
	// - admitted but not yet graduated to a real interval. It has no other
	// job: it does not shrink the total amount of work and does not look at
	// cards from other days - it only paces how fast unseen cards trickle
	// into the sitting you are already in, and how many you are juggling at
	// once while they're still being learned, so a big deck doesn't dump
	// every new word on you back to back. The instant an admitted card
	// graduates, its slot goes to the next one in held. 0 disables the gate
	// and admits every new due card immediately.
	LearningPool int

	// StepGap is how many other cards are shown before a card that is still
	// mid-learning-step comes back. Clamped to the end of the queue when
	// fewer than that remain.
	StepGap int
}

// NewSession builds the running order for one sitting: every due card that
// has been seen before, plus as many new words as the learning pool has room
// for right now. More are let in during the sitting as admitted ones
// graduate and free their slot - see topUp - so the sitting always ends with
// every due card, seen or not, shown at least once. The pool only ever
// changes the order things arrive in, never whether they do.
func NewSession(cards []Card, cfg SessionConfig) *Session {
	s := &Session{cfg: cfg, pending: map[string]bool{}}
	for _, c := range cards {
		if !IsDue(c) {
			continue
		}
		if c.State == fsrs.New {
			s.held = append(s.held, c.Key())
		} else {
			s.queue = append(s.queue, c.Key())
		}
	}
	shuffle(s.queue)
	shuffle(s.held)
	s.topUp(cards)
	return s
}

// NewStudyAheadSession builds a sitting out of the cards that are NOT due yet,
// soonest first.
//
// This is the answer to a deck where everything is scheduled a few days out:
// the alternative is a "Done!" screen for six days running. It is deliberately
// something you opt into after being told nothing is due, rather than
// somewhere the app slides you automatically - reviewing early costs you the
// spacing that made the card stick, so it should be a decision.
//
// Sorted rather than shuffled, because with study-ahead the order is the whole
// point: the cards closest to falling due are the ones worth pulling forward,
// and you stop whenever you like. The learning pool is not consulted - every
// card here has been seen before, so there is nothing to introduce.
func NewStudyAheadSession(cards []Card, cfg SessionConfig) *Session {
	s := &Session{cfg: cfg, pending: map[string]bool{}}
	ahead := make([]Card, 0, len(cards))
	for _, c := range cards {
		if !IsDue(c) {
			ahead = append(ahead, c)
		}
	}
	slices.SortStableFunc(ahead, func(a, b Card) int { return a.Due.Compare(b.Due) })
	for _, c := range ahead {
		s.queue = append(s.queue, c.Key())
	}
	return s
}

// CountStudyAhead is how many cards a study-ahead sitting would offer.
func CountStudyAhead(cards []Card) int {
	n := 0
	for _, c := range cards {
		if !IsDue(c) {
			n++
		}
	}
	return n
}

func shuffle(keys []string) {
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
}

// inFlight counts admitted words this sitting is still working on - the
// trickle's only limiter. It is scoped to s.pending (this sitting's own
// admissions), not the whole deck: cards from other days, or reviews that
// were already due before the sitting opened, never took a pool slot and
// have no bearing on how fast unseen cards trickle in today.
//
// A word occupies its slot from admission until it graduates - ScheduledDays
// turns nonzero, FSRS's own signal that it now trusts a real interval rather
// than "ask again in a few minutes" (see requeue). Being touched once is not
// enough to free the slot: under the short-term scheduler a word often takes
// two or more ratings to graduate, and until it does it is still work in
// progress, not a slot to hand to something else. Checked live rather than
// trusting admission order, since a rating can graduate a word on the spot.
func (s *Session) inFlight(cards []Card) int {
	n := 0
	for key := range s.pending {
		if c := FindCard(cards, key); c != nil && c.ScheduledDays == 0 {
			n++
		}
	}
	return n
}

// topUp lets new words in for as long as the pool has room.
//
// This runs after every rating, not just at the start of the sitting: the
// moment an admitted word graduates, its slot frees and the next one in held
// takes it, in the same sitting. That is the entire mechanism - there is no
// day boundary, so held always fully drains into queue by the end of a
// sitting; the pool only spreads out when that happens, never whether it
// does.
func (s *Session) topUp(cards []Card) {
	if s.cfg.LearningPool <= 0 {
		// Gate off: everything due, all at once.
		s.queue = append(s.queue, s.held...)
		s.held = nil
		return
	}
	for len(s.held) > 0 && s.inFlight(cards) < s.cfg.LearningPool {
		s.admit()
	}
}

// admit moves the next held word into the sitting. It goes somewhere in what
// is left to do, rather than always at the end, so new words are spread
// through the sitting instead of queueing up behind the whole review backlog.
func (s *Session) admit() {
	key := s.held[0]
	s.held = s.held[1:]
	s.pending[key] = true
	at := s.pos + rand.Intn(len(s.queue)-s.pos+1)
	s.queue = slices.Insert(s.queue, at, key)
}

// CountStudyable reports how many cards a session started now would open
// with, before any trickling in happens. CountDueCards is the true backlog -
// due reviews plus every not-yet-introduced word - and is what the deck list
// shows; this is the smaller opening batch the pool hands over first. It is a
// floor, not the total the sitting will eventually get through: held cards
// keep trickling into the queue as admitted ones are rated.
func CountStudyable(cards []Card, cfg SessionConfig) int {
	return NewSession(cards, cfg).Remaining()
}

// Current returns the card to show, or nil when the sitting is over.
//
// cards is passed in rather than held because the web UI re-reads the deck
// from disk on every request; the queue identifies cards by key so either
// copy works.
func (s *Session) Current(cards []Card) *Card {
	if s == nil || s.pos >= len(s.queue) {
		return nil
	}
	return FindCard(cards, s.queue[s.pos])
}

// Rate applies a rating to the current card and advances.
//
// The card keeps its place in the long-term schedule through Review, exactly
// as before. What is new is that a card which is still mid-step goes back
// into this sitting's queue instead of vanishing until its due date.
func (s *Session) Rate(cards []Card, rating fsrs.Rating) {
	if s == nil || s.pos >= len(s.queue) {
		return
	}
	key := s.queue[s.pos]
	c := FindCard(cards, key)
	if c != nil {
		Review(c, rating)
	}
	s.pos++

	if c != nil && (rating == fsrs.Again || c.ScheduledDays == 0) {
		s.requeue(key)
	}
	// If that rating just graduated a pool word, its slot is free now - so
	// the next held word can come in immediately, same sitting.
	s.topUp(cards)
}

// requeue puts a card back into the sitting, StepGap cards further on.
//
// ScheduledDays == 0 is FSRS saying "this one is in a learning step, due in
// minutes" - so the queue hands it back rather than making anyone sit out the
// clock. The long-term scheduler never produces a 0, so there the condition
// in Rate reduces to "Again comes back" and this is the same code either way.
//
// Rating Again (or Hard, mid-step) forever therefore keeps the card in the
// sitting forever. That is deliberate: the sitting ends when you stop failing
// the card, which is what Anki does too.
func (s *Session) requeue(key string) {
	at := min(s.pos+s.cfg.StepGap, len(s.queue))
	s.queue = slices.Insert(s.queue, at, key)
}

// Remaining is how many cards are left to show, re-queued ones included.
func (s *Session) Remaining() int {
	if s == nil {
		return 0
	}
	return len(s.queue) - s.pos
}

// Position is how far through the sitting we are, as "card n of total".
// Both numbers grow when a card is re-queued.
func (s *Session) Position() (n, total int) {
	if s == nil {
		return 0, 0
	}
	return s.pos + 1, len(s.queue)
}
