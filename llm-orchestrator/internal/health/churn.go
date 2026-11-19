package health

import (
	"bytes"
	"context"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// History sampling: the last commits only, and commits small enough to say
// something about which files belong together.
const (
	maxCommits        = 500
	maxCommitFiles    = 20
	minPairCommits    = 3
	minPairCoincident = 0.6
)

// history is what the recent commits tell about each file.
type history struct {
	commits int
	churn   map[string]int // commits touching each file
	pairs   [][2]string    // files that keep changing together
}

// readHistory reads the last maxCommits commits of repo. A directory that is
// not a git repository has no history, which is not an error.
func readHistory(ctx context.Context, repo string) (history, error) {
	h := history{churn: map[string]int{}}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "log", "-n", strconv.Itoa(maxCommits), "--no-merges",
		"--format=%x00", "--name-only")
	out, err := cmd.Output()
	if err != nil {
		if !isRepo(ctx, repo) {
			return h, nil
		}
		return h, err
	}
	together := map[[2]string]int{}
	for _, chunk := range bytes.Split(out, []byte{0}) {
		files := commitFiles(chunk)
		if len(files) == 0 {
			continue
		}
		h.commits++
		for _, f := range files {
			h.churn[f]++
		}
		if len(files) > maxCommitFiles {
			continue
		}
		for i := range files {
			for j := i + 1; j < len(files); j++ {
				together[[2]string{files[i], files[j]}]++
			}
		}
	}
	for pair, n := range together {
		least := min(h.churn[pair[0]], h.churn[pair[1]])
		if n >= minPairCommits && float64(n) >= minPairCoincident*float64(least) {
			h.pairs = append(h.pairs, pair)
		}
	}
	slices.SortFunc(h.pairs, func(a, b [2]string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	return h, nil
}

// commitFiles lists one commit's files, sorted and unique.
func commitFiles(chunk []byte) []string {
	var files []string
	for _, line := range strings.Split(string(chunk), "\n") {
		if line = strings.TrimSpace(line); line != "" && !slices.Contains(files, line) {
			files = append(files, line)
		}
	}
	slices.Sort(files)
	return files
}

func isRepo(ctx context.Context, repo string) bool {
	return exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--git-dir").Run() == nil
}
