package computers

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type fakeLine struct{}

func (fakeLine) send([]application.WorkspaceInput) <-chan outcome {
	return settled(outcome{result: contracts.ComputerServerMessageResultResultTrue{}})
}
func (fakeLine) query(contracts.ComputerQuery) <-chan outcome { return settled(outcome{}) }
func (fakeLine) reconnect()                                   {}
func (fakeLine) stop()                                        {}

type storedFile struct {
	Name      *string `json:"name"`
	Computers []struct {
		Name  string  `json:"name"`
		Label *string `json:"label"`
		Host  string  `json:"host"`
		Node  *string `json:"node"`
		Token string  `json:"token"`
	} `json:"computers"`
}

func readFile(t *testing.T, file string) storedFile {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var held storedFile
	if err := jsonx.Unmarshal(data, &held); err != nil {
		t.Fatal(err)
	}
	return held
}

func TestComputerLongUnreachableIsFoundUnderItsNewTailnetName(t *testing.T) {
	file := filepath.Join(t.TempDir(), ComputersFile)
	if err := os.WriteFile(file, []byte(`{"version":1,"computers":[{"id":"box","name":"linux-box","host":"linux-box.tail.ts.net","token":"token","pairedAt":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var dialled []string
	var statuses []func(domain.ComputerStatus)
	peers := []TailnetPeer{{ID: "node-1", Host: "linux-box.tail.ts.net", Name: "linux-box", OS: "linux", Online: true}}
	links := NewComputerLinks(LinksOptions{
		File:       file,
		DeviceName: "This Mac",
		Peers: func(context.Context) ([]TailnetPeer, error) {
			mu.Lock()
			defer mu.Unlock()
			return peers, nil
		},
		connect: func(options ClientOptions) line {
			mu.Lock()
			defer mu.Unlock()
			dialled = append(dialled, options.Host)
			statuses = append(statuses, func(status domain.ComputerStatus) { options.OnStatus(status, nil) })
			return fakeLine{}
		},
	})
	t.Cleanup(links.Stop)
	links.Start()
	links.loop.call(func() { statuses[0](domain.ComputerStatusConnected) })
	until(t, "the node learned on connecting", func() bool {
		held := readFile(t, file)
		return len(held.Computers) == 1 && held.Computers[0].Node != nil && *held.Computers[0].Node == "node-1"
	})
	links.loop.call(func() { statuses[0](domain.ComputerStatusOffline) })
	mu.Lock()
	peers = []TailnetPeer{{ID: "node-1", Host: "linux-box-1.tail.ts.net", Name: "linux-box", OS: "linux", Online: true}}
	mu.Unlock()
	now := application.Now
	later := now() + 2*60_000
	links.loop.call(func() { application.Now = func() float64 { return later } })
	t.Cleanup(func() { links.loop.call(func() { application.Now = now }) })
	links.Reconnect(nil)
	until(t, "dialling the new name", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(dialled) == 2
	})
	if !reflect.DeepEqual(dialled, []string{"linux-box.tail.ts.net", "linux-box-1.tail.ts.net"}) {
		t.Fatal(dialled)
	}
	if host := links.Links()[0].Host; host != "linux-box-1.tail.ts.net" {
		t.Fatal(host)
	}
}

func TestLinksKeepTheTokenOnDiskAndAForgottenComputerIsCutOff(t *testing.T) {
	served := startElectronHost(t)
	folder := t.TempDir()
	var mu sync.Mutex
	var changes [][]string
	states := map[string]*application.WorkspaceState{}
	links := NewComputerLinks(LinksOptions{
		File:       filepath.Join(folder, ComputersFile),
		DeviceName: "This Mac",
		OnChanged: func(_ string, held []*domain.ComputerLink) {
			mu.Lock()
			defer mu.Unlock()
			change := []string{}
			for _, link := range held {
				change = append(change, link.Name+":"+string(link.Status))
			}
			changes = append(changes, change)
		},
		OnState: func(id string, state *application.WorkspaceState) {
			mu.Lock()
			defer mu.Unlock()
			states[id] = state
		},
		connect: func(options ClientOptions) line {
			options.URL = served.url
			c := newClient(options)
			c.open()
			return c
		},
	})
	t.Cleanup(links.Stop)
	links.Start()
	mu.Lock()
	if !reflect.DeepEqual(changes, [][]string{{}}) {
		t.Fatal(changes)
	}
	mu.Unlock()
	if err := links.Pair(background(), "other.tail.ts.net", "linux-box", served.mint()); err != nil {
		t.Fatal(err)
	}
	until(t, "the paired computer coming online", func() bool {
		mu.Lock()
		defer mu.Unlock()
		last := changes[len(changes)-1]
		return len(last) == 1 && last[0] == "linux-box:connected"
	})
	id := links.Links()[0].ID
	until(t, "the paired computer's state arriving", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return states[id] != nil
	})
	held := readFile(t, filepath.Join(folder, ComputersFile))
	if held.Computers[0].Name != "linux-box" || held.Computers[0].Host != "other.tail.ts.net" || held.Computers[0].Token == "" {
		t.Fatal(held)
	}
	if err := links.Pair(background(), "other.tail.ts.net", "linux-box", "AGAIN"); err == nil || !strings.Contains(err.Error(), "already paired") {
		t.Fatal(err)
	}
	if err := links.Forget(id); err != nil {
		t.Fatal(err)
	}
	if len(links.Links()) != 0 {
		t.Fatal("still held")
	}
	if held := readFile(t, filepath.Join(folder, ComputersFile)); len(held.Computers) != 0 {
		t.Fatal(held)
	}
	if _, err := links.Send(background(), id, []application.WorkspaceInput{contracts.TaskCommandTaskNew{}}); err == nil || !strings.Contains(err.Error(), "no longer paired") {
		t.Fatal(err)
	}
}

func TestStoredFileIsWrittenAsTheElectronAppWritesIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "profile", ComputersFile)
	links := NewComputerLinks(LinksOptions{File: file, DeviceName: "zhuo-mac", connect: func(ClientOptions) line { return fakeLine{} }})
	links.loop.call(func() {
		links.ownName = new("Studio")
		links.hold(storedComputer{ID: "a", Name: "box", Label: new("Box"), Host: "box.ts.net", Node: new("n1"), Token: "t", PairedAt: 1_700_000_000_000})
		links.hold(storedComputer{ID: "b", Name: "mini", Host: "mini.ts.net", Token: "u", PairedAt: 2})
	})
	if err := links.Rename("Studio 2"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	want := `{"version":1,"name":"Studio 2","computers":[{"id":"a","name":"box","label":"Box","host":"box.ts.net","node":"n1","token":"t","pairedAt":1700000000000},{"id":"b","name":"mini","host":"mini.ts.net","token":"u","pairedAt":2}]}`
	if string(data) != want {
		t.Fatalf("\n got %s\nwant %s", data, want)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
}

func namedLinks(t *testing.T, url, folder string, onChanged func(string, []*domain.ComputerLink)) *ComputerLinks {
	t.Helper()
	links := NewComputerLinks(LinksOptions{
		File:       filepath.Join(folder, ComputersFile),
		DeviceName: "zhuo-mac",
		OnChanged:  onChanged,
		connect: func(options ClientOptions) line {
			options.URL = url
			c := newClient(options)
			c.open()
			return c
		},
	})
	t.Cleanup(links.Stop)
	links.Start()
	return links
}

func TestThisComputersNameIsKeptOnDiskUntilCleared(t *testing.T) {
	folder := t.TempDir()
	var mu sync.Mutex
	changes := 0
	first := namedLinks(t, "ws://127.0.0.1:1/m/workspace", folder, func(string, []*domain.ComputerLink) {
		mu.Lock()
		defer mu.Unlock()
		changes++
	})
	if first.Name() != "zhuo-mac" {
		t.Fatal(first.Name())
	}
	_ = first.Rename("  Studio  ")
	if first.Name() != "Studio" || *readFile(t, filepath.Join(folder, ComputersFile)).Name != "Studio" {
		t.Fatal(first.Name())
	}
	if changes != 2 {
		t.Fatalf("a new name is announced like any other change: %d", changes)
	}
	_ = first.Rename("Studio")
	if changes != 2 {
		t.Fatal("the same name again is not")
	}
	first.Stop()
	again := namedLinks(t, "ws://127.0.0.1:1/m/workspace", folder, nil)
	if again.Name() != "Studio" {
		t.Fatal("the name outlives the process")
	}
	_ = again.Rename("")
	if again.Name() != "zhuo-mac" || readFile(t, filepath.Join(folder, ComputersFile)).Name != nil {
		t.Fatal(again.Name())
	}
}

func TestPairedComputersOwnNameArrivesAndFollowsRenamesUnlessLabelled(t *testing.T) {
	served := startElectronHost(t)
	folder := t.TempDir()
	file := filepath.Join(folder, ComputersFile)
	var mu sync.Mutex
	var names []string
	held := namedLinks(t, served.url, folder, func(_ string, links []*domain.ComputerLink) {
		mu.Lock()
		defer mu.Unlock()
		shown := []string{}
		for _, link := range links {
			shown = append(shown, link.Name)
		}
		names = append(names, strings.Join(shown, ","))
	})
	lastName := func() string {
		mu.Lock()
		defer mu.Unlock()
		return names[len(names)-1]
	}
	if err := held.Pair(background(), "127.0.0.1", "127.0.0.1", served.mint()); err != nil {
		t.Fatal(err)
	}
	until(t, "the computer's own name replacing the address it was paired by", func() bool { return held.Links()[0].Name == "linux-box" })
	if readFile(t, file).Computers[0].Name != "linux-box" {
		t.Fatal("not kept")
	}
	id := held.Links()[0].ID
	served.call("rename", map[string]any{"name": "Build box"})
	until(t, "the rename crossing the live line", func() bool { return held.Links()[0].Name == "Build box" })
	if readFile(t, file).Computers[0].Name != "Build box" {
		t.Fatal("not kept")
	}
	_ = held.Label(id, " The Linux one ")
	if held.Links()[0].Name != "The Linux one" || lastName() != "The Linux one" {
		t.Fatal(held.Links()[0].Name)
	}
	labelled := readFile(t, file).Computers[0]
	if labelled.Name != "Build box" || *labelled.Label != "The Linux one" {
		t.Fatal(labelled)
	}
	served.call("rename", map[string]any{"name": "Renamed there"})
	until(t, "what that computer calls itself kept underneath", func() bool { return readFile(t, file).Computers[0].Name == "Renamed there" })
	if held.Links()[0].Name != "The Linux one" || lastName() != "The Linux one" {
		t.Fatal("the label stands over the rename")
	}
	_ = held.Label(id, "")
	if held.Links()[0].Name != "Renamed there" || readFile(t, file).Computers[0].Label != nil {
		t.Fatal("clearing the label goes back to what that computer calls itself")
	}
	_ = held.Label(id, "Renamed there")
	if readFile(t, file).Computers[0].Label != nil {
		t.Fatal("a label that is the computer's own name is no label")
	}
}

func TestServedHostReadsTheNameChosenInTheApp(t *testing.T) {
	file := filepath.Join(t.TempDir(), ComputersFile)
	if StoredComputerName(file, "zhuo-mac") != "zhuo-mac" {
		t.Fatal("no file yet")
	}
	links := NewComputerLinks(LinksOptions{File: file, DeviceName: "zhuo-mac"})
	_ = links.Rename("Studio")
	if StoredComputerName(file, "zhuo-mac") != "Studio" {
		t.Fatal("not read")
	}
	_ = links.Rename("")
	if StoredComputerName(file, "zhuo-mac") != "zhuo-mac" {
		t.Fatal("a cleared name is the machine's again")
	}
	_ = os.WriteFile(file, []byte("not json"), 0o600)
	if StoredComputerName(file, "zhuo-mac") != "zhuo-mac" {
		t.Fatal("an unreadable file is no name")
	}
	_ = os.WriteFile(file, []byte(`{"version":1,"name":"","computers":[]}`), 0o600)
	if StoredComputerName(file, "zhuo-mac") != "zhuo-mac" {
		t.Fatal("an empty name is no name")
	}
}

func TestStoredComputersAreReadLeniently(t *testing.T) {
	file := filepath.Join(t.TempDir(), ComputersFile)
	_ = os.WriteFile(file, []byte(`{"version":1,"computers":[{"id":"a","name":"n","host":"h","token":"t","pairedAt":1},{"id":"b","name":"n","host":"h","token":"t"},{"id":"c","name":"n","label":5,"host":"h","token":"t","pairedAt":1},null]}`), 0o600)
	_, computers := readStored(file)
	if len(computers) != 1 || computers[0].ID != "a" {
		t.Fatal(computers)
	}
}
