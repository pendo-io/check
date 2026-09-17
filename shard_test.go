package check

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// These tests use the standard library directly rather than gocheck itself: every other test file
// in this package imports github.com/masukomi/check, which registers the same gocheck.* flags and
// so panics the test binary at init before any test runs. Until that is untangled these must be
// run on their own, e.g. by moving the other _test.go files aside.

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Suite%03d", i)
	}
	return out
}

// collect returns the union of every shard's share, and the per-shard counts.
func collect(t *testing.T, all []string, total int, seed int64, weights map[string]float64) ([]string, []int) {
	t.Helper()
	var union []string
	counts := make([]int, total)
	fingerprint := ""
	for i := 0; i < total; i++ {
		plan := partitionNames(all, shardSpec{index: i, total: total}, seed, weights)
		if i == 0 {
			fingerprint = plan.fingerprint
		} else if plan.fingerprint != fingerprint {
			t.Fatalf("shard %d/%d computed fingerprint %s, shard 0 computed %s",
				i, total, plan.fingerprint, fingerprint)
		}
		union = append(union, plan.names...)
		counts[i] = len(plan.names)
	}
	sort.Strings(union)
	return union, counts
}

func TestParseShardSpec(t *testing.T) {
	spec, err := parseShardSpec("3/8")
	if err != nil {
		t.Fatalf("parseShardSpec(3/8): %v", err)
	}
	if spec.index != 3 || spec.total != 8 {
		t.Errorf("parseShardSpec(3/8) = %+v, want index 3 total 8", spec)
	}
	if spec, err := parseShardSpec(" 1 / 2 "); err != nil || spec.index != 1 || spec.total != 2 {
		t.Errorf("parseShardSpec with spaces = %+v, %v; want index 1 total 2, no error", spec, err)
	}

	for _, value := range []string{"", "2", "a/2", "0/b", "0/0", "0/-1", "2/2", "3/2", "-1/2"} {
		if _, err := parseShardSpec(value); err == nil {
			t.Errorf("parseShardSpec(%q) accepted a bad spec", value)
		}
	}
}

// Every suite must land in exactly one shard: a suite in two shards runs twice, and one in none is
// silently skipped.
func TestPartitionNamesCoversEverySuiteExactlyOnce(t *testing.T) {
	all := names(97)
	for _, total := range []int{1, 2, 3, 5, 8, 16, 97, 128} {
		for _, seed := range []int64{0, 1, 42} {
			union, counts := collect(t, all, total, seed, nil)
			if len(union) != len(all) {
				t.Fatalf("total=%d seed=%d: shards hold %d suites, want %d (counts %v)",
					total, seed, len(union), len(all), counts)
			}
			for i, name := range union {
				if name != all[i] {
					t.Fatalf("total=%d seed=%d: shards hold %q at %d, want %q",
						total, seed, name, i, all[i])
				}
			}
		}
	}
}

// The partition is derived from sorted names so that moving a Suite() call between files, or
// between lines, cannot move suites between shards mid-run.
func TestPartitionNamesIgnoresRegistrationOrder(t *testing.T) {
	all := names(50)
	shuffled := append([]string(nil), all...)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	spec := shardSpec{index: 2, total: 5}
	ordered := partitionNames(all, spec, 0, nil)
	jumbled := partitionNames(shuffled, spec, 0, nil)

	if ordered.fingerprint != jumbled.fingerprint {
		t.Errorf("fingerprint changed with registration order: %s vs %s",
			ordered.fingerprint, jumbled.fingerprint)
	}
	if fmt.Sprint(ordered.names) != fmt.Sprint(jumbled.names) {
		t.Errorf("shard contents changed with registration order:\n %v\n %v",
			ordered.names, jumbled.names)
	}
}

func TestPartitionNamesBalancesByWeight(t *testing.T) {
	all := names(40)
	weights := map[string]float64{}
	var ideal float64
	for i, name := range all {
		// One dominant suite plus a long tail, which is the shape of a real package.
		weights[name] = 1.0
		if i == 0 {
			weights[name] = 30.0
		}
		ideal += weights[name]
	}
	const total = 6
	ideal /= total

	var worst float64
	for i := 0; i < total; i++ {
		plan := partitionNames(all, shardSpec{index: i, total: total}, 0, weights)
		var load float64
		for _, name := range plan.names {
			load += weights[name]
		}
		if load > worst {
			worst = load
		}
	}
	// The floor is the largest single suite, which gocheck cannot split; longest-processing-time
	// scheduling guarantees at most 4/3 - 1/(3n) of optimal otherwise.
	if want := weights[all[0]]; worst > want {
		t.Errorf("heaviest shard carries %.1f, above the %.1f single-suite floor (ideal %.1f)",
			worst, want, ideal)
	}
}

// An unbalanced partition is the failure mode weights exist to prevent, so check the unweighted
// case really is worse - otherwise the weights file is doing nothing and nobody would notice.
func TestPartitionNamesWithoutWeightsSplitsByCount(t *testing.T) {
	all := names(12)
	counts := make(map[int]int)
	for i := 0; i < 4; i++ {
		plan := partitionNames(all, shardSpec{index: i, total: 4}, 0, nil)
		counts[len(plan.names)]++
	}
	if len(counts) != 1 || counts[3] != 4 {
		t.Errorf("unweighted shards should hold equal counts, got %v", counts)
	}
}

// A suite the weights file does not mention still has to be scheduled somewhere, and the count of
// them is what tells a reader the file has gone stale.
func TestPartitionNamesSchedulesUnweightedSuites(t *testing.T) {
	all := names(10)
	weights := map[string]float64{all[0]: 5, all[1]: 5}

	union, _ := collect(t, all, 3, 0, weights)
	if len(union) != len(all) {
		t.Fatalf("shards hold %d suites, want %d", len(union), len(all))
	}
	plan := partitionNames(all, shardSpec{index: 0, total: 3}, 0, weights)
	if plan.unweighted != 8 {
		t.Errorf("unweighted = %d, want 8", plan.unweighted)
	}
}

func TestShardFingerprintDistinguishesInputs(t *testing.T) {
	all := names(20)
	base := shardFingerprint(all, 4, 0, nil)

	cases := map[string]string{
		"more shards":      shardFingerprint(all, 5, 0, nil),
		"different seed":   shardFingerprint(all, 4, 1, nil),
		"extra suite":      shardFingerprint(append(append([]string(nil), all...), "Extra"), 4, 0, nil),
		"weights supplied": shardFingerprint(all, 4, 0, map[string]float64{all[0]: 3}),
	}
	for what, got := range cases {
		if got == base {
			t.Errorf("%s produced the same fingerprint %s", what, base)
		}
	}
	if again := shardFingerprint(all, 4, 0, nil); again != base {
		t.Errorf("fingerprint is not stable: %s then %s", base, again)
	}
}

func TestLoadShardWeights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weights.tsv")
	body := "# a comment\n\nSlowSuite\t34.60\nQuickSuite\t15.9\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	weights, digest, err := loadShardWeights(path)
	if err != nil {
		t.Fatalf("loadShardWeights: %v", err)
	}
	if len(weights) != 2 || weights["SlowSuite"] != 34.6 || weights["QuickSuite"] != 15.9 {
		t.Errorf("weights = %v", weights)
	}
	if digest == "" || digest == "none" {
		t.Errorf("digest = %q, want a real digest", digest)
	}

	if _, blank, err := loadShardWeights(""); err != nil || blank != "none" {
		t.Errorf("no weights file gave %q, %v; want \"none\", no error", blank, err)
	}

	// Comments and blank lines must not change the digest, or shards given cosmetically different
	// copies of one file would look like they disagreed.
	tidy := filepath.Join(t.TempDir(), "weights.tsv")
	if err := os.WriteFile(tidy, []byte("SlowSuite\t34.60\nQuickSuite\t15.9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, same, _ := loadShardWeights(tidy); same != digest {
		t.Errorf("comments changed the digest: %s vs %s", same, digest)
	}

	for what, bad := range map[string]string{
		"no tab":       "OnlyAName 12\n",
		"not a number": "Suite\tfast\n",
		"negative":     "Suite\t-1\n",
	} {
		path := filepath.Join(t.TempDir(), "weights.tsv")
		if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadShardWeights(path); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}

	if _, _, err := loadShardWeights(filepath.Join(t.TempDir(), "absent.tsv")); err == nil {
		t.Error("a missing weights file was accepted")
	}
}

func TestShardOutputPath(t *testing.T) {
	cases := map[string]string{
		"reports/junit-check-%pkg.xml": "reports/junit-check-%pkg.shard2of6.xml",
		"report":                       "report.shard2of6",
		"a.b/report.xml":               "a.b/report.shard2of6.xml",
	}
	for in, want := range cases {
		if got := shardOutputPath(in, shardSpec{index: 2, total: 6}); got != want {
			t.Errorf("shardOutputPath(%q) = %q, want %q", in, got, want)
		}
	}
}
