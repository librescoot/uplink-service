package commands

import (
	"reflect"
	"testing"
)

func TestResolveUpdateTargetsDefaultsToBothBoards(t *testing.T) {
	got, err := resolveUpdateTargets(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mdb", "dbc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveUpdateTargetsOrchestratedBothCollapsesToMDB(t *testing.T) {
	got, err := resolveUpdateTargets([]string{"mdb", "dbc"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mdb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveUpdateTargetsDBCSingleStaysDirect(t *testing.T) {
	got, err := resolveUpdateTargets([]string{"dbc"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"dbc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveUpdateTargetsRejectsUnknownBoard(t *testing.T) {
	if _, err := resolveUpdateTargets([]string{"mdb", "sdcard"}, false); err == nil {
		t.Fatal("want error for unknown board")
	}
}
