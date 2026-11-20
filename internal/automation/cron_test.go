package automation

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// croner.json is croner 11's own answers, recorded under TZ=America/Los_Angeles.
type cronerFixture struct {
	Cases []struct {
		Pattern  string   `json:"pattern"`
		Timezone string   `json:"timezone"`
		Start    string   `json:"start"`
		Next     []string `json:"next"`
	} `json:"cases"`
	ErrorCases []struct {
		Pattern string  `json:"pattern"`
		Error   *string `json:"error"`
	} `json:"errorCases"`
	OnceCases []struct {
		Schedule string `json:"schedule"`
		Timezone string `json:"timezone"`
		At       string `json:"at"`
	} `json:"onceCases"`
}

func loadCronerFixture(t *testing.T) cronerFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/croner.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture cronerFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func withLocal(t *testing.T, name string) {
	t.Helper()
	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	held := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = held })
}

func TestPatternsFireWhenCronerFiresThem(t *testing.T) {
	fixture := loadCronerFixture(t)
	failures := 0
	for _, c := range fixture.Cases {
		pattern, err := parseCronPattern(c.Pattern)
		if err != nil {
			t.Fatalf("%q: %v", c.Pattern, err)
		}
		zone, err := cronZone(c.Timezone)
		if err != nil {
			t.Fatal(err)
		}
		at, _ := time.Parse(time.RFC3339Nano, c.Start)
		var got []string
		for range c.Next {
			next, ok := pattern.next(at, zone)
			if !ok {
				break
			}
			got = append(got, next.UTC().Format("2006-01-02T15:04:05.000Z"))
			at = next
		}
		if len(got) != len(c.Next) || func() bool {
			for index := range got {
				if got[index] != c.Next[index] {
					return true
				}
			}
			return false
		}() {
			failures++
			if failures <= 20 {
				t.Errorf("%q in %s from %s: got %v, croner %v", c.Pattern, c.Timezone, c.Start, got, c.Next)
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d cases differ", failures, len(fixture.Cases))
	}
}

func TestPatternErrorsMatchCroner(t *testing.T) {
	for _, c := range loadCronerFixture(t).ErrorCases {
		_, err := parseCronPattern(c.Pattern)
		switch {
		case c.Error == nil && err != nil:
			t.Errorf("%q: croner accepts it, got %v", c.Pattern, err)
		case c.Error != nil && err == nil:
			t.Errorf("%q: croner refuses it with %q", c.Pattern, *c.Error)
		case c.Error != nil && err.Error() != *c.Error:
			t.Errorf("%q:\n got %q\nwant %q", c.Pattern, err.Error(), *c.Error)
		}
	}
}

func TestOneShotsLandWhereCronerPutsThem(t *testing.T) {
	withLocal(t, "America/Los_Angeles")
	for _, c := range loadCronerFixture(t).OnceCases {
		cron, err := newCron(c.Schedule, cronOptions{timezone: c.Timezone, paused: true}, nil)
		if err != nil {
			t.Fatalf("%q: %v", c.Schedule, err)
		}
		if got := cron.once.UTC().Format("2006-01-02T15:04:05.000Z"); got != c.At {
			t.Errorf("%q in %s: got %s, croner %s", c.Schedule, c.Timezone, got, c.At)
		}
	}
}

func TestOneShotAnswersUntilASecondPastAndNotAfterItRan(t *testing.T) {
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	cron, err := newCron(at.UTC().Format(time.RFC3339), cronOptions{paused: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := at.Add(900 * time.Millisecond)
	cron.now = func() time.Time { return clock }
	if next, ok := cron.NextRun(); !ok || !next.Equal(at) {
		t.Fatalf("a moment under a second past still counts: %v %v", next, ok)
	}
	clock = at.Add(1100 * time.Millisecond)
	if _, ok := cron.NextRun(); ok {
		t.Fatal("a moment over a second past is gone")
	}
	clock = at.Add(-time.Minute)
	ran := at
	cron.currentRun = &ran
	if _, ok := cron.NextRun(); ok {
		t.Fatal("a one-shot that ran has no next run")
	}
}

func TestATimerFiresAndProtectsAgainstOverrun(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	protected := make(chan struct{}, 4)
	at := time.Now().Add(1100 * time.Millisecond).Truncate(time.Second)
	cron, err := newCron(at.UTC().Format(time.RFC3339Nano), cronOptions{protect: func() { protected <- struct{}{} }}, func() error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cron.Stop()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the one-shot never fired")
	}
	if !cron.IsBusy() {
		t.Fatal("a run in flight is busy")
	}
	if _, ok := cron.NextRun(); ok {
		t.Fatal("a fired one-shot has no next run")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for cron.IsBusy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cron.IsBusy() {
		t.Fatal("a settled run is no longer busy")
	}
	if len(protected) != 0 {
		t.Fatal("nothing overran")
	}
}
