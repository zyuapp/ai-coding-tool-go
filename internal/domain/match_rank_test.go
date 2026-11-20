package domain

import (
	"reflect"
	"testing"
)

func TestRankedMatches(t *testing.T) {
	type item struct{ name, host string }
	items := []item{{"billing-api", ""}, {"web-app", ""}, {"api-gateway", ""}, {"ml-pipeline", "linux-box"}}
	names := func(i item) []string { return []string{i.name, i.host} }
	of := func(found []item) []string {
		out := []string{}
		for _, i := range found {
			out = append(out, i.name)
		}
		return out
	}
	if got := of(RankedMatches(items, "API", names)); !reflect.DeepEqual(got, []string{"api-gateway", "billing-api"}) {
		t.Fatal(got)
	}
	if got := of(RankedMatches(items, "linux", names)); !reflect.DeepEqual(got, []string{"ml-pipeline"}) {
		t.Fatal(got)
	}
	if got := RankedMatches(items, "  ", names); !reflect.DeepEqual(got, items) {
		t.Fatal(got)
	}
	if got := RankedMatches(items, "zzz", names); len(got) != 0 {
		t.Fatal(got)
	}
}
