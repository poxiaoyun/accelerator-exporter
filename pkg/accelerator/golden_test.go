package accelerator

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden.txt from the current implementation")

// TestDeviceLayerGolden locks the device-layer label set of the whole fixture
// corpus.
//
// The label set is the contract downstream consumers build on, so it is pinned
// as one artifact rather than as a handful of per-metric assertions: an
// accidental extra label, a missing identity group, or an ownership label
// reappearing on a device metric all show up as a diff here.
func TestDeviceLayerGolden(t *testing.T) {
	t.Parallel()
	got := renderFixtures(t)
	path := filepath.Join("testdata", "golden.txt")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (re-run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("device layer changed; if this is intended re-run with -update.\n--- got ---\n%s", got)
	}
}

func renderFixtures(t *testing.T) string {
	t.Helper()
	var builder strings.Builder
	for _, fixture := range fixtures {
		result := applyFixture(t, fixture)
		fmt.Fprintf(&builder, "== %s\n", fixture.name)
		lines := make([]string, 0, len(result.Samples))
		for _, sample := range result.Samples {
			names := make([]string, 0, len(sample.Labels))
			for name := range sample.Labels {
				names = append(names, name)
			}
			sort.Strings(names)
			pairs := make([]string, 0, len(names))
			for _, name := range names {
				pairs = append(pairs, name+"="+sample.Labels[name])
			}
			lines = append(lines, fmt.Sprintf("%s{%s} %v", sample.Name, strings.Join(pairs, ","), sample.Value))
		}
		sort.Strings(lines)
		for _, line := range lines {
			builder.WriteString("  " + line + "\n")
		}
	}
	return builder.String()
}
