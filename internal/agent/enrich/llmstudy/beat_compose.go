package llmstudy

import (
	"strconv"
	"strings"
)

// The composition pass writes the prose beat from the two passes above and from the measured
// record — and from NOTHING ELSE. It never sees the conversation.
//
// That is the point of splitting at all. A prose writer holding the raw window can embroider:
// every phrase in the transcript is available to it, including the ones that make a progress
// claim sound supported, and no check downstream can prove which of two plausible sentences the
// window actually licensed. A prose writer holding a list of typed names and a list of events
// cannot invent a fact it was never shown — the material it has is the material it can use, and
// what it was shown is recorded, so the beat is checkable against its own inputs rather than
// against 16,000 runes of conversation.
//
// The truth statuses are stated separately for the same reason the report prompts state theirs:
// the record's counts are measured on device and authoritative, the entity names are measured
// and verbatim-verified while their KINDS are a reading, and the events are a reading of the
// conversation. Collapsing those into one authoritative-sounding block is the fabricated-
// authority failure this study has already paid for.
//
// BeatComposePrompt takes no window argument, and that is the primary guarantee — not a rule in
// the prompt text, which a model can ignore, but a signature that has nowhere to put one. The
// test that would fail if a window leaked in is TestComposePassNeverSeesTheWindow.

const (
	beatEntityHeader = "NAMES IN THIS WORK (measured on device — each name occurs verbatim in " +
		"the session; the kind beside it is a reading — indicative):\n"
	beatEventHeader = "WHAT THIS STRETCH SHOWS HAPPENING (read off the conversation — " +
		"indicative):\n"
	beatRecordHeader = "SESSION RECORD (measured — authoritative):\n"
)

// BeatComposePrompt asks for the beat, from the passes only.
func BeatComposePrompt(record string, entities []BeatEntity, events []string) string {
	var b strings.Builder
	b.WriteString("A colleague asks you at standup what you are working on. Answer them in two " +
		"or three sentences, from the notes below.\n\n")
	if names := RenderBeatEntities(entities); names != "" {
		b.WriteString(beatEntityHeader)
		b.WriteString(names)
		b.WriteString("\n")
	}
	if list := RenderBeatEvents(events); list != "" {
		b.WriteString(beatEventHeader)
		b.WriteString(list)
		b.WriteString("\n")
	}
	if record != "" {
		b.WriteString(beatRecordHeader)
		b.WriteString(record)
	}
	b.WriteString(`
Rules:
  - Answer the way a person answers that question out loud: two or three sentences, plainly.
  - Begin with the thing being worked on, named. Vary how you open.
  - Say what the work IS — the subject, and what it is part of — using the kinds above to
    place each name.
  - Then say what happened, exactly as the notes have it.
  - Every noun comes from the notes above; they are all you have seen of this session.
    Nothing in these instructions is subject matter.
  - Finish every sentence. End the last one with a full stop.
  - No preamble, no headings, no bullets. Plain prose only.

Respond with JSON only.
`)
	p := b.String()
	assertBeatPromptWithinBudget(p)
	return p
}

// BeatSplit is one beat produced by the split passes, with every input and every output of every
// pass — the artifact records this whole struct, so a reader can see what the prose was written
// from rather than inferring it.
type BeatSplit struct {
	Candidates     []string     `json:"candidates"`
	EntityPrompt   string       `json:"entity_prompt,omitempty"`
	Entities       []BeatEntity `json:"entities,omitempty"`
	EntityUnjudged []string     `json:"entity_unjudged,omitempty"`
	EntityAttempts int          `json:"entity_attempts"`
	EntityErr      string       `json:"entity_err,omitempty"`

	EventPrompt   string   `json:"event_prompt,omitempty"`
	Events        []string `json:"events,omitempty"`
	EventAttempts int      `json:"event_attempts"`
	EventErr      string   `json:"event_err,omitempty"`

	ComposePrompt   string `json:"compose_prompt,omitempty"`
	Raw             string `json:"raw,omitempty"`
	Text            string `json:"text,omitempty"`
	ComposeAttempts int    `json:"compose_attempts"`
	ComposeErr      string `json:"compose_err,omitempty"`

	// Ungrounded are the distinctive terms the beat uses that do NOT occur in the composition
	// prompt. It is the measurement of the constraint this design claims: with the window
	// withheld, a beat should not be able to name anything the passes did not hand it. Recorded
	// rather than enforced — a rejection here would hide the very thing worth counting.
	Ungrounded []string `json:"ungrounded,omitempty"`
	// ProgressClaims is what the shape check found in the STORED beat, empty when it found
	// nothing. Non-empty means an offending generation survived, which is a defect.
	ProgressClaims []string `json:"progress_claims,omitempty"`
}

// Failed reports whether any pass failed, and Which names the first that did.
func (s BeatSplit) Failed() bool { return s.Which() != "" }

// Which names the failing pass, or "" when all three succeeded.
func (s BeatSplit) Which() string {
	switch {
	case s.EntityErr != "":
		return "entity"
	case s.EventErr != "":
		return "event"
	case s.ComposeErr != "":
		return "compose"
	}
	return ""
}

// Attempts is what the whole beat cost in requests, across the three passes.
func (s BeatSplit) Attempts() int { return s.EntityAttempts + s.EventAttempts + s.ComposeAttempts }

// GenerateBeatSplit runs the three passes and returns everything they produced.
//
// The error is returned AND recorded on the struct, because a failed beat is part of what
// happened: the artifact shows failures in place with their attempt counts, and a caller that
// only got an error could not say which pass burned them.
func (l *Llama) GenerateBeatSplit(rec SessionRecord, window string) (BeatSplit, error) {
	var s BeatSplit
	record := rec.Block()

	before := l.Attempts()
	cands, entities, unjudged, prompt, err := l.GenerateBeatEntities(rec, window)
	s.Candidates, s.Entities, s.EntityUnjudged, s.EntityPrompt = cands, entities, unjudged, prompt
	s.EntityAttempts = l.Attempts() - before
	if err != nil {
		s.EntityErr = err.Error()
		return s, err
	}

	before = l.Attempts()
	events, eprompt, err := l.GenerateBeatEvents(window)
	s.Events, s.EventPrompt = events, eprompt
	s.EventAttempts = l.Attempts() - before
	if err != nil {
		s.EventErr = err.Error()
		return s, err
	}

	s.ComposePrompt = BeatComposePrompt(record, entities, events)
	var out struct {
		Beat string `json:"beat"`
	}
	before = l.Attempts()
	err = l.callValidSampled(s.ComposePrompt, BeatSchema(), &out, func() error {
		s.Raw, s.Text = strings.TrimSpace(out.Beat), ClipBeat(out.Beat, BeatCap)
		switch {
		case s.Raw == "":
			return passProblem("beat", "beat is empty")
		case s.Text == "":
			return passProblem("beat", "beat holds no complete sentence within "+
				strconv.Itoa(BeatCap)+" runes")
		case runeLen(s.Text) < BeatMinRunes:
			return passProblem("beat", "beat is "+strconv.Itoa(runeLen(s.Text))+
				" runes after dropping its incomplete tail, under the floor of "+
				strconv.Itoa(BeatMinRunes))
		// The evidence handed to the progress check is the COMPOSITION PROMPT, which is
		// exactly and provably what this beat was written from. On the control path the
		// evidence is the window plus the record for the same reason; here the window is not
		// evidence the writer had.
		case BeatClaimsUnobservableProgress(s.Text, s.ComposePrompt):
			return passProblem("beat", "beat characterises overall progress the notes "+
				"do not show: "+strings.Join(beatProgressClaims(s.Text), "; "))
		}
		return nil
	}, beatSampling)
	s.ComposeAttempts = l.Attempts() - before
	if err != nil {
		s.ComposeErr = err.Error()
		s.Text = ""
		return s, err
	}
	s.ProgressClaims = beatProgressClaims(s.Text)
	s.Ungrounded = ungroundedTerms(s.Text, s.ComposePrompt)
	return s, nil
}

// ungroundedTerms returns the distinctive terms of text that do not occur in source.
//
// Reuses the two existing gates rather than inventing a third: distinctiveToken decides which of
// a beat's tokens are specifics worth checking, and VerifyTopics is the verbatim occurrence test
// the publish path already trusts for spans. So "ungrounded" here means the same thing it means
// everywhere else in this package — the term cannot be located in what the model was shown.
func ungroundedTerms(text, source string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, tok := range subjectTokens(text) {
		if !distinctiveToken(tok) {
			continue
		}
		t := trimTermPunct(tok)
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			terms = append(terms, t)
		}
	}
	_, dropped := VerifyTopics(terms, source)
	return dropped
}
