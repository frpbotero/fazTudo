//go:build desktop

package main

import "testing"

func TestMergePathKeepsOrderAndDropsDuplicates(t *testing.T) {
	got := mergePath("/a:/b", "/b::/c", "/a:/d")
	if want := "/a:/b:/c:/d"; got != want {
		t.Fatalf("mergePath = %q, want %q", got, want)
	}
}
