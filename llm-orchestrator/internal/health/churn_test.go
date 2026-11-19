package health

import (
	"context"
	"slices"
	"testing"
)

func TestHistoryCountsChurnAndPairsThatChangeTogether(t *testing.T) {
	r := newTestRepo(t)
	for range 3 {
		r.commit("pay/a.go", "pay/b.go") // always together
	}
	r.commit("pay/a.go")
	r.commit("cart/c.go", "pay/a.go") // together once: not a pair
	big := make([]string, maxCommitFiles+1)
	for i := range big {
		big[i] = "gen/f" + string(rune('a'+i)) + ".go"
	}
	for range 3 {
		r.commit(big...) // too many files per commit to count as pairs
	}

	h, err := readHistory(context.Background(), r.root)
	if err != nil {
		t.Fatal(err)
	}
	if h.commits != 8 || h.churn["pay/a.go"] != 5 || h.churn["pay/b.go"] != 3 || h.churn["gen/fa.go"] != 3 {
		t.Errorf("commits = %d churn = %v", h.commits, h.churn)
	}
	want := [][2]string{{"pay/a.go", "pay/b.go"}}
	if !slices.Equal(h.pairs, want) {
		t.Errorf("pairs = %v, want %v", h.pairs, want)
	}
}

func TestHistoryOfADirectoryWithoutGit(t *testing.T) {
	h, err := readHistory(context.Background(), t.TempDir())
	if err != nil || h.commits != 0 {
		t.Errorf("no git: history = %+v, err = %v", h, err)
	}
}
