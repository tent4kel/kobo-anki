package core

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-spaced-repetition/go-fsrs/v3"
)

// useScheduler switches the package-level scheduler for one test and puts it
// back afterwards. The two modes schedule Again completely differently, and
// several of the properties below have to hold under both.
func useScheduler(t *testing.T, shortTerm bool) {
	t.Helper()
	old := scheduler
	t.Cleanup(func() { scheduler = old })
	InitScheduler(0.9, 36500, shortTerm)
}

// seenCard is a card that has been reviewed before and is due now, scheduled
// scheduledDays out from its last review.
func seenCard(id string, scheduledDays int) Card {
	return Card{
		ID:            id,
		Front:         id,
		State:         fsrs.Review,
		Due:           time.Now().Add(-time.Hour),
		LastReview:    time.Now().AddDate(0, 0, -scheduledDays),
		ScheduledDays: uint64(scheduledDays),
		Stability:     float64(scheduledDays),
		Difficulty:    5,
		Reps:          2,
	}
}

// futureCard is like seenCard but not yet due - still in flight, just not
// today's problem.
func futureCard(id string, scheduledDays int) Card {
	c := seenCard(id, scheduledDays)
	c.Due = time.Now().Add(24 * time.Hour)
	c.LastReview = time.Now()
	return c
}

// newCard is a card as kobo-vocab writes it: no due date, state New.
func newCard(id string) Card {
	return Card{ID: id, Front: id, State: fsrs.New}
}

func deck(build func(add func(Card))) []Card {
	var cards []Card
	build(func(c Card) { cards = append(cards, c) })
	return cards
}

// drain walks a session to the end, rating each card with rate, and returns
// the order the cards were presented in. The step limit keeps a bug in the
// re-queue logic from hanging the suite.
func drain(t *testing.T, s *Session, cards []Card, rate func(key string) fsrs.Rating) []string {
	t.Helper()
	var order []string
	for range 200 {
		c := s.Current(cards)
		if c == nil {
			return order
		}
		order = append(order, c.Key())
		s.Rate(cards, rate(c.Key()))
	}
	t.Fatalf("session did not end after 200 cards; order so far: %v", order)
	return nil
}

func count(keys []string, want string) int {
	n := 0
	for _, k := range keys {
		if k == want {
			n++
		}
	}
	return n
}

// The reported bug: rating Again used to be the one way to guarantee a card
// would NOT come back, because the queue was just "due date has passed" and
// every rating - Again included - is scheduled at least a day out under the
// long-term scheduler, or five minutes out under the short-term one.
func TestAgainBringsTheCardBackInTheSameSitting(t *testing.T) {
	for _, shortTerm := range []bool{false, true} {
		name := "long-term"
		if shortTerm {
			name = "short-term"
		}
		t.Run(name, func(t *testing.T) {
			useScheduler(t, shortTerm)

			cards := deck(func(add func(Card)) {
				add(seenCard("a", 10))
				add(seenCard("b", 10))
				add(seenCard("c", 10))
			})
			s := NewSession(cards, SessionConfig{StepGap: 1})

			failed := s.Current(cards).Key()
			// Fail it once, then pass everything from then on.
			seen := false
			order := drain(t, s, cards, func(key string) fsrs.Rating {
				if key == failed && !seen {
					seen = true
					return fsrs.Again
				}
				return fsrs.Good
			})

			if got := count(order, failed); got != 2 {
				t.Errorf("failed card %q was shown %d time(s), want 2: %v", failed, got, order)
			}
			// FSRS still owns the long-term schedule: the card is dated into
			// the future even though the session showed it again.
			c := FindCard(cards, failed)
			if !c.Due.After(time.Now()) {
				t.Errorf("%q is due %v, want a date in the future", failed, c.Due)
			}
		})
	}
}

// Under the short-term scheduler a new card graduates in two steps, and both
// belong to the same sitting: the first Good leaves it in Learning with
// ScheduledDays == 0 (due in ten minutes), which is the queue's cue to hand
// it straight back rather than make anyone wait out the clock.
func TestNewCardWalksItsLearningStepsInOneSitting(t *testing.T) {
	useScheduler(t, true)

	cards := deck(func(add func(Card)) { add(newCard("wort")) })
	s := NewSession(cards, SessionConfig{LearningPool: 10, StepGap: 3})

	order := drain(t, s, cards, func(string) fsrs.Rating { return fsrs.Good })

	if len(order) != 2 {
		t.Fatalf("shown %d time(s), want 2 (one learning step, then graduation): %v", len(order), order)
	}
	c := FindCard(cards, "wort")
	if c.ScheduledDays == 0 {
		t.Errorf("card did not graduate: ScheduledDays still 0, due %v", c.Due)
	}
	if c.State != fsrs.Review {
		t.Errorf("state is %v, want Review", c.State)
	}
}

// Under the long-term scheduler ScheduledDays is never 0, so a passed card is
// finished with for the sitting.
func TestAPassedCardDoesNotComeBack(t *testing.T) {
	useScheduler(t, false)

	for _, rating := range []fsrs.Rating{fsrs.Hard, fsrs.Good, fsrs.Easy} {
		cards := deck(func(add func(Card)) {
			add(seenCard("a", 10))
			add(seenCard("b", 10))
		})
		s := NewSession(cards, SessionConfig{StepGap: 1})
		order := drain(t, s, cards, func(string) fsrs.Rating { return rating })
		if len(order) != 2 {
			t.Errorf("%v: shown %d cards, want 2: %v", rating, len(order), order)
		}
	}
}

// StepGap is a target, not a promise - with fewer cards left than the gap
// asks for, the re-queued card goes to the end rather than off it.
func TestRequeueClampsToTheEndOfTheQueue(t *testing.T) {
	useScheduler(t, false)

	cards := deck(func(add func(Card)) {
		add(seenCard("a", 10))
		add(seenCard("b", 10))
	})
	s := NewSession(cards, SessionConfig{StepGap: 10})

	failed := s.Current(cards).Key()
	s.Rate(cards, fsrs.Again)

	if got := s.Current(cards).Key(); got == failed {
		t.Fatalf("re-queued card came back immediately, want the other card first")
	}
	s.Rate(cards, fsrs.Good)
	if got := s.Current(cards); got == nil || got.Key() != failed {
		t.Fatalf("re-queued card is not at the end of the queue: %v", got)
	}
}

// The pool caps new admissions, but every already-seen due card is included
// unconditionally - reviews never compete with new cards for a pool slot.
func TestNewCardsFillOnlyTheFreeSlotsAlongsideEveryDueReview(t *testing.T) {
	cards := deck(func(add func(Card)) {
		for _, id := range []string{"a1", "a2", "a3"} {
			add(seenCard(id, 1))
		}
		for i := range 20 {
			add(newCard(string(rune('A' + i))))
		}
	})

	q := NewSession(cards, SessionConfig{LearningPool: 5}).queue
	if len(q) != 8 {
		t.Fatalf("queue has %d cards, want 3 due reviews + 5 new (the pool) = 8: %v", len(q), q)
	}
	fresh := 0
	for _, key := range q {
		if FindCard(cards, key).State == fsrs.New {
			fresh++
		}
	}
	if fresh != 5 {
		t.Errorf("admitted %d new cards, want 5 (the whole pool - reviews don't share it): %v", fresh, q)
	}
}

// Rating an admitted new card - any rating, not just a passing one - takes it
// out of fsrs.New immediately, which is what frees its slot for the next held
// word, same sitting. There is no multi-day maturity check anywhere in this.
// Under the long-term scheduler, every grade - including Again - lands on a
// real (nonzero) interval on the very first rating: Parameters.nextInterval
// floors at 1 day regardless of stability. So touching and graduating a card
// are the same event here, whatever grade it gets, and its pool slot frees
// immediately.
func TestALongTermRatingGraduatesAndFreesItsSlotImmediately(t *testing.T) {
	useScheduler(t, false)

	for _, grade := range []fsrs.Rating{fsrs.Again, fsrs.Hard, fsrs.Good, fsrs.Easy} {
		cards := deck(func(add func(Card)) {
			add(newCard("erste"))
			add(newCard("zweite"))
		})
		s := NewSession(cards, SessionConfig{LearningPool: 1, StepGap: 3})

		if got := s.Remaining(); got != 1 {
			t.Fatalf("%v: sitting opened with %d cards, want just the one the pool admits", grade, got)
		}
		admitted := s.Current(cards).Key()

		s.Rate(cards, grade)

		if sd := FindCard(cards, admitted).ScheduledDays; sd == 0 {
			t.Errorf("%v: long-term rating left ScheduledDays at 0, want a real (if short) interval", grade)
		}
		if len(s.pending) != 2 {
			t.Errorf("%v: freed slot did not let the other new word in: pending=%d, want 2", grade, len(s.pending))
		}
	}
}

// Under the short-term scheduler, a card can be touched more than once before
// it graduates - and until it does, it keeps its pool slot. Rating it (even
// with a passing grade that doesn't finish the learning step) must not admit
// a replacement on top of it: that was the collision between the pool and
// short-term learning steps that let a sitting's working set balloon past
// LearningPool.
func TestAnUngraduatedCardKeepsItsSlotUntilItGraduates(t *testing.T) {
	useScheduler(t, true)

	cards := deck(func(add func(Card)) {
		for i := range 6 {
			add(newCard(string(rune('A' + i))))
		}
	})
	s := NewSession(cards, SessionConfig{LearningPool: 1, StepGap: 3})

	if len(s.pending) != 1 || len(s.held) != 5 {
		t.Fatalf("test setup: want exactly 1 admitted and 5 left in held, got pending=%d held=%d",
			len(s.pending), len(s.held))
	}
	admitted := s.Current(cards).Key()

	s.Rate(cards, fsrs.Hard) // first touch; short-term Hard repeats the step

	if sd := FindCard(cards, admitted).ScheduledDays; sd != 0 {
		t.Fatalf("test setup: %q graduated after one Hard (ScheduledDays=%d), want still mid-step", admitted, sd)
	}
	if len(s.pending) != 1 || len(s.held) != 5 {
		t.Errorf("a replacement was admitted before %q graduated: pending=%d held=%d, want unchanged (1, 5)",
			admitted, len(s.pending), len(s.held))
	}
}

// The pool never blocks the sitting from finishing: it only paces the order
// new words arrive in. Every one of them gets shown by the end, one at a time
// as each admitted word is rated and frees its slot.
func TestAFullPoolStillDrainsEveryNewWordBeforeTheSittingEnds(t *testing.T) {
	useScheduler(t, true)

	cards := deck(func(add func(Card)) {
		for i := range 20 {
			add(newCard(string(rune('A' + i))))
		}
	})
	s := NewSession(cards, SessionConfig{LearningPool: 2, StepGap: 3})

	order := drain(t, s, cards, func(string) fsrs.Rating { return fsrs.Good })

	introduced := map[string]bool{}
	for _, k := range order {
		introduced[k] = true
	}
	if len(introduced) != 20 {
		t.Errorf("introduced %d distinct words, want all 20 - the pool paces intake, it doesn't cap it: %v",
			len(introduced), order)
	}
}

// A future (not-yet-due) card has no bearing on the pool at all: it was never
// part of this sitting's held or pending, so it neither takes a slot nor
// needs to "mature" out of one. Only this sitting's own new-card admissions
// count.
func TestCardsNotDueTodayDoNotOccupyThePool(t *testing.T) {
	cards := deck(func(add func(Card)) {
		for _, id := range []string{"a1", "a2", "a3"} {
			add(futureCard(id, 1))
		}
		for i := range 20 {
			add(newCard(string(rune('A' + i))))
		}
	})

	q := NewSession(cards, SessionConfig{LearningPool: 5}).queue
	if len(q) != 5 {
		t.Fatalf("queue has %d cards, want 0 due reviews + 5 new (full pool, nothing held it back) = 5: %v", len(q), q)
	}
}

// The case that used to make the deck list lie: nothing due besides a pile of
// new words. A sitting now opens with one pool's worth and, as you work
// through it, trickles in the rest - the whole backlog, not just the first
// batch - rather than stopping at the pool size and calling it Done.
func TestASittingWorksThroughTheWholeBacklogNotJustOnePoolsWorth(t *testing.T) {
	useScheduler(t, true)

	cards := deck(func(add func(Card)) {
		for i := range 37 {
			add(newCard(fmt.Sprintf("neu%02d", i)))
		}
	})
	cfg := SessionConfig{LearningPool: 10, StepGap: 3}

	s := NewSession(cards, cfg)
	if got := s.Remaining(); got != 10 {
		t.Fatalf("sitting opened with %d cards, want a pool's worth (10) as the opening batch", got)
	}

	order := drain(t, s, cards, func(string) fsrs.Rating { return fsrs.Good })
	introduced := map[string]bool{}
	for _, k := range order {
		introduced[k] = true
	}
	if len(introduced) != 37 {
		t.Errorf("worked through %d words, want all 37 - the pool paces arrival, it never leaves any behind", len(introduced))
	}
}

func TestLearningPoolZeroOffersEveryDueCard(t *testing.T) {
	cards := deck(func(add func(Card)) {
		for i := range 20 {
			add(newCard(string(rune('A' + i))))
		}
	})

	if q := NewSession(cards, SessionConfig{LearningPool: 0}).queue; len(q) != 20 {
		t.Errorf("queue has %d cards, want all 20 with the gate off", len(q))
	}
}

// The property RandomDueCard could not provide: it drew at random with
// replacement on every advance, so a card could come up twice in a row while
// another due card was never shown at all.
func TestEveryDueCardIsShownExactlyOnce(t *testing.T) {
	useScheduler(t, false)

	var ids []string
	cards := deck(func(add func(Card)) {
		for i := range 10 {
			id := string(rune('a' + i))
			ids = append(ids, id)
			add(seenCard(id, 10))
		}
	})

	s := NewSession(cards, SessionConfig{StepGap: 3})
	order := drain(t, s, cards, func(string) fsrs.Rating { return fsrs.Good })

	for _, id := range ids {
		if got := count(order, id); got != 1 {
			t.Errorf("%q shown %d time(s), want exactly 1: %v", id, got, order)
		}
	}
}

// A deck where everything is scheduled a few days out offers nothing at all -
// the case study-ahead exists for. Order is the point here: soonest first, so
// the cards closest to falling due are the ones you pull forward.
func TestStudyAheadOffersFutureCardsSoonestFirst(t *testing.T) {
	useScheduler(t, false)

	cards := deck(func(add func(Card)) {
		for _, days := range []int{30, 2, 6, 1} {
			c := seenCard(fmt.Sprintf("in%dd", days), 10)
			c.Due = time.Now().AddDate(0, 0, days)
			add(c)
		}
	})
	cfg := SessionConfig{LearningPool: 10, StepGap: 3}

	// Nothing is due, so an ordinary sitting is empty.
	if got := NewSession(cards, cfg).Remaining(); got != 0 {
		t.Fatalf("ordinary sitting offered %d cards, want none - nothing is due", got)
	}
	if got := CountStudyAhead(cards); got != 4 {
		t.Errorf("CountStudyAhead = %d, want 4", got)
	}

	s := NewStudyAheadSession(cards, cfg)
	order := drain(t, s, cards, func(string) fsrs.Rating { return fsrs.Good })

	want := []string{"in1d", "in2d", "in6d", "in30d"}
	if !slices.Equal(order, want) {
		t.Errorf("presented %v, want %v (soonest first)", order, want)
	}
}

// Study-ahead is only for cards that are not due. A card that IS due belongs
// to the ordinary sitting and must not be offered twice.
func TestStudyAheadSkipsCardsThatAreAlreadyDue(t *testing.T) {
	cards := deck(func(add func(Card)) {
		add(seenCard("due", 10))
		add(futureCard("later", 10))
	})

	s := NewStudyAheadSession(cards, SessionConfig{LearningPool: 10})
	if got := s.queue; !slices.Equal(got, []string{"later"}) {
		t.Errorf("study-ahead queue is %v, want just the card that is not due", got)
	}
}

func TestCountStudyableMatchesWhatTheSessionHandsOver(t *testing.T) {
	useScheduler(t, false)

	cards := deck(func(add func(Card)) {
		add(seenCard("a", 1))
		add(seenCard("b", 1))
		add(futureCard("c", 1)) // not due - irrelevant to both counts
		for i := range 20 {
			add(newCard(string(rune('A' + i))))
		}
	})
	cfg := SessionConfig{LearningPool: 5, StepGap: 3}

	// The two due reviews plus a pool's worth of new cards (5) is the opening
	// batch - not the 22 cards whose due date has technically passed.
	want := CountStudyable(cards, cfg)
	if due := CountDueCards(cards); due == want {
		t.Fatalf("CountStudyable and CountDueCards agree (%d); the gate is not doing anything", want)
	}

	s := NewSession(cards, cfg)
	if got := s.Remaining(); got != want {
		t.Errorf("session hands over %d cards, CountStudyable promised %d", got, want)
	}
}

func TestLearningModeSettingsAreRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anki-core.conf")
	os.WriteFile(p, []byte("learning_pool=25\nstep_gap=5\n"), 0644)

	cfg := LoadCoreConfig(p)
	if want := (SessionConfig{LearningPool: 25, StepGap: 5}); cfg.Session() != want {
		t.Errorf("Session() = %+v, want %+v", cfg.Session(), want)
	}
}

// A config left over from before mature_interval was removed should be
// flagged, not silently ignored - it no longer means anything, and a leftover
// setting nobody's reading is exactly what ConfigWarnings exists to surface.
func TestRemovedMatureIntervalSettingWarns(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anki-core.conf")
	os.WriteFile(p, []byte("mature_interval=21\n"), 0644)

	out := captureLog(t, func() { LoadCoreConfig(p) })
	if !strings.Contains(out, "mature_interval") {
		t.Errorf("no warning for the removed setting:\n%s", out)
	}
}

func TestLearningModeSettingsWarnOnBadValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "anki-core.conf")
	os.WriteFile(p, []byte("learning_pool=lots\n"), 0644)

	var cfg CoreConfig
	out := captureLog(t, func() { cfg = LoadCoreConfig(p) })

	if !strings.Contains(out, `learning_pool="lots"`) {
		t.Errorf("no warning for the unparseable value:\n%s", out)
	}
	if cfg.LearningPool != 10 {
		t.Errorf("LearningPool = %d, want the default 10 kept", cfg.LearningPool)
	}
}
