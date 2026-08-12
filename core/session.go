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

	// held is the new cards this deck could introduce, in the order they
	// would be, minus the ones already let in. The pool is a rolling window
	// rather than a quota for the sitting: the moment a card matures out of
	// it, the next word here takes its slot.
	held []string

	// pending marks new cards already let into the queue. They hold a slot
	// from the moment they are introduced - not from the moment they are
	// first answered - or a single freed slot would let the whole deck in.
	// Entries are never removed: once a card is answered it stops being New
	// and IsActive counts it instead, so the State test below retires it.
	pending map[string]bool
}

// SessionConfig is the learning-mode policy. The zero value is a session with
// no new-card gate and no re-queueing, i.e. the old behaviour.
type SessionConfig struct {
	// LearningPool caps how many cards may be in flight - seen but not yet
	// learned - at once. New cards are admitted only to fill the remaining
	// slots. 0 disables the gate and offers every due card.
	LearningPool int

	// MatureInterval is the scheduled interval, in days, at which a card
	// counts as learned and frees its slot in the pool.
	MatureInterval int

	// StepGap is how many other cards are shown before a card that is still
	// mid-learning-step comes back. Clamped to the end of the queue when
	// fewer than that remain.
	StepGap int
}

// IsMature reports whether FSRS trusts this card for at least matureInterval
// days - the point at which it stops occupying a slot in the learning pool.
//
// This reads ScheduledDays rather than State on purpose. State looks like the
// natural answer (Learning means learning) but it only works under the
// short-term scheduler: the long-term one sends every rated card straight to
// Review and never uses Learning or Relearning at all. ScheduledDays means
// the same thing under both - and a card mid-step has 0, which correctly
// reads as not mature.
func IsMature(c Card, matureInterval int) bool {
	return c.State != fsrs.New && c.ScheduledDays >= uint64(matureInterval)
}

// IsActive reports whether a card is in flight: seen at least once, not yet
// mature. These are the cards the learning pool counts.
func IsActive(c Card, matureInterval int) bool {
	return c.State != fsrs.New && !IsMature(c, matureInterval)
}

// NewSession builds the running order for one sitting: every due card that
// has been seen before, plus as many new words as the learning pool has room
// for right now. More are let in during the sitting as room appears.
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

	// The pool paces how fast new words arrive; it does not get to leave you
	// with nothing to do while words are waiting. A sitting can otherwise open
	// empty for days at a time - the pool sitting over its cap because those
	// cards were learned before it existed, or simply every review being a few
	// days out - and the deck list would be advertising work the app then
	// refuses to hand over.
	//
	// So when nothing else is studiable, one sitting's worth comes in
	// regardless of the cap. That is bounded: those words are in flight the
	// moment they are introduced, so inPool is over the cap for the rest of
	// the sitting and topUp admits nothing more.
	if len(s.queue) == 0 {
		for len(s.held) > 0 && len(s.queue) < s.cfg.LearningPool {
			s.admit()
		}
	}
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

// inPool counts the cards occupying the learning pool: everything in flight
// anywhere in the deck, plus the new words already let into this sitting.
//
// It is counted over the whole deck, not just the due cards, because a word
// you are three days into learning still holds its slot on the days it is not
// due - otherwise the pool would empty and refill every morning.
func (s *Session) inPool(cards []Card) int {
	n := 0
	for _, c := range cards {
		if IsActive(c, s.cfg.MatureInterval) || (c.State == fsrs.New && s.pending[c.Key()]) {
			n++
		}
	}
	return n
}

// topUp lets new words in for as long as the pool has room.
//
// This runs after every rating, not just at the start of the sitting, which
// is what makes the pool a rolling window: the moment a card matures out of
// it, the next word takes its place, in the same sitting. Each maturing card
// frees exactly one slot and so admits exactly one word, so the sitting grows
// only in proportion to the work done in it.
func (s *Session) topUp(cards []Card) {
	if s.cfg.LearningPool <= 0 {
		// Gate off: everything due, all at once.
		s.queue = append(s.queue, s.held...)
		s.held = nil
		return
	}
	for len(s.held) > 0 && s.inPool(cards) < s.cfg.LearningPool {
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

// CountStudyable reports how many cards a session started now would open with.
//
// This is what a deck list should show. CountDueCards still answers the
// literal question "how many cards are past their due date", but with a
// learning pool in force that number can be the whole deck while the session
// opens with ten. It is a floor, not a total: a sitting that matures cards out
// of the pool picks up new words as it goes.
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
	// That rating may have pushed a card past mature_interval and out of the
	// pool. If it did, its slot is free now, not next sitting.
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
