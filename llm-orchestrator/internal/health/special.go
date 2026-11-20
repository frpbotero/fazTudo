package health

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// specialKind names one family of special cases: code written for one
// situation instead of following from how the system works.
type specialKind struct {
	label string
	re    *regexp.Regexp
}

// specialKinds are matched line by line in code files of any language.
var specialKinds = []specialKind{
	{"sleep", regexp.MustCompile(`\btime\.Sleep\(|\bsetTimeout\(|\bThread\.sleep\(|\b(?:time\.|asyncio\.)?sleep\(|\busleep\(`)},
	{"retry", regexp.MustCompile(`(?i)\bretr(?:y|ies)\b|\bmaxRetries\b|\bbackoff\b`)},
	{"teste pulado", regexp.MustCompile(`\bt\.Skip(?:f|Now)?\(|\b(?:it|test|describe)\.skip\(|\bx(?:it|describe|test)\(|@Disabled\b|@pytest\.mark\.skip|@Ignore\b|\bpending\(`)},
	{"exceção engolida", regexp.MustCompile(`catch\s*(?:\([^)]*\))?\s*\{\s*\}|except[^:\n]*:\s*pass\b|\b_\s*=\s*err\b|rescue\s*(?:=>\s*\w+)?\s*$`)},
}

// maxScanBytes skips files too big to be hand-written code.
const maxScanBytes = 1 << 20

// countSpecial counts the special-case lines of one file, per kind.
func countSpecial(root, rel string) map[string]int {
	info, err := os.Stat(filepath.Join(root, rel))
	if err != nil || info.Size() > maxScanBytes {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		for _, k := range specialKinds {
			if k.re.MatchString(line) {
				counts[k.label]++
			}
		}
	}
	return counts
}
