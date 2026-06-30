package help_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ggkguelensan/workspace-isolation/internal/help"
)

// TestForOverviewListsEveryCommand + TestForCommandDetail + TestForUnknownTopic +
// TestNextIsRunnable + TestTableIsFullyPopulated are the HELP-MODEL fitness guard. They
// pin the progressive-disclosure contract: the empty topic yields the overview listing
// the full table, a known command yields its synopsis/usage/next, an unknown topic is
// ok=false, every next[] entry is a runnable `wi …` line, and no table row carries blank
// metadata (so help can never lie about the command surface).
//
// Non-vacuity mutant (any one reddens the suite): (a) make For ignore its topic and
// always return the overview — TestForCommandDetail's Usage/Next/Commands-nil checks and
// TestForUnknownTopic redden; (b) make For return ok=true for an unknown topic —
// TestForUnknownTopic reddens; (c) drop the "wi " prefix on Next entries —
// TestNextIsRunnable reddens; (d) leave a row's Usage/Next empty —
// TestTableIsFullyPopulated reddens.

func TestForOverviewListsEveryCommand(t *testing.T) {
	m, ok := help.For("")
	if !ok {
		t.Fatal(`For("") = ok false, want the overview`)
	}
	if m.Topic != "" {
		t.Errorf("overview Topic = %q, want empty", m.Topic)
	}
	if m.Synopsis == "" {
		t.Error("overview Synopsis is empty; the top-level help must introduce wi")
	}
	if m.Usage != "" {
		t.Errorf("overview Usage = %q, want empty (the overview has no single usage)", m.Usage)
	}
	if !reflect.DeepEqual(m.Commands, help.Commands()) {
		t.Errorf("overview Commands = %#v, want the full table %#v", m.Commands, help.Commands())
	}
	if len(m.Commands) == 0 {
		t.Fatal("overview lists no commands; the table is empty")
	}
}

func TestForCommandDetail(t *testing.T) {
	// Pick a representative one-token and two-token command straight from the table so
	// this test never hard-codes prose that the table owns.
	for _, want := range help.Commands() {
		t.Run(want.Name, func(t *testing.T) {
			m, ok := help.For(want.Name)
			if !ok {
				t.Fatalf("For(%q) = ok false, want the command detail", want.Name)
			}
			if m.Topic != want.Name {
				t.Errorf("Topic = %q, want %q", m.Topic, want.Name)
			}
			if m.Synopsis != want.Synopsis {
				t.Errorf("Synopsis = %q, want %q", m.Synopsis, want.Synopsis)
			}
			if m.Usage != want.Usage {
				t.Errorf("Usage = %q, want %q", m.Usage, want.Usage)
			}
			if !reflect.DeepEqual(m.Next, want.Next) {
				t.Errorf("Next = %#v, want %#v", m.Next, want.Next)
			}
			if m.Commands != nil {
				t.Errorf("command detail Commands = %#v, want nil (no re-listing the whole table)", m.Commands)
			}
		})
	}
}

func TestForUnknownTopic(t *testing.T) {
	m, ok := help.For("frobnicate")
	if ok {
		t.Errorf("For(unknown) = ok true (%#v), want ok false so the caller can refuse with did_you_mean", m)
	}
	if !reflect.DeepEqual(m, help.Model{}) {
		t.Errorf("For(unknown) = %#v, want the zero Model", m)
	}
}

func TestNextIsRunnable(t *testing.T) {
	// Every next[] entry the model emits — overview and per-command — must be a command
	// line runnable verbatim, i.e. start with "wi " (the envelope next[] contract).
	check := func(t *testing.T, where string, next []string) {
		for _, n := range next {
			if !strings.HasPrefix(n, "wi ") {
				t.Errorf("%s next entry %q is not a runnable `wi …` command", where, n)
			}
		}
	}
	overview, _ := help.For("")
	if len(overview.Next) == 0 {
		t.Error("overview Next is empty; it must point at a getting-started step")
	}
	check(t, "overview", overview.Next)
	for _, c := range help.Commands() {
		if len(c.Next) == 0 {
			t.Errorf("command %q has no Next; every command must suggest a follow-up", c.Name)
		}
		check(t, c.Name, c.Next)
	}
}

func TestTableIsFullyPopulated(t *testing.T) {
	for _, c := range help.Commands() {
		if c.Name == "" || c.Synopsis == "" || c.Usage == "" {
			t.Errorf("table row %#v has blank metadata; help would lie about the command surface", c)
		}
	}
}
