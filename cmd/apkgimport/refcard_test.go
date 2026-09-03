package main

import (
	"strings"
	"testing"
)

// The back face is the only one that can ever contain Anki's stock
// "#answer" marker (from {{FrontSide}}<hr id=answer>{{Back}}) - cards.css
// needs a way to scope a rule to "the back face's repeated front" without
// matching the front face's own .card too, and litehtml has no :has() to
// detect #answer's presence from the outside. So the back face's wrapper
// needs its own class the front face's wrapper doesn't get.
func TestRefCardBackFaceGetsDistinguishingClass(t *testing.T) {
	got := refCard("myslug", "<p>answer body</p>", true)

	if !strings.Contains(got, `class="card kobo-anki-back"`) {
		t.Fatalf("refCard(back=true) = %q, want a wrapper with class %q", got, "card kobo-anki-back")
	}
}

func TestRefCardFrontFaceHasPlainCardClass(t *testing.T) {
	got := refCard("myslug", "<p>question body</p>", false)

	if !strings.Contains(got, `class="card"`) {
		t.Fatalf("refCard(back=false) = %q, want a wrapper with class %q", got, "card")
	}
	if strings.Contains(got, "kobo-anki-back") {
		t.Fatalf("refCard(back=false) = %q, front face must not carry the back-only class", got)
	}
}
