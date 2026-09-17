package check

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Sharding splits a package's registered suites across several test processes so they can run in
// parallel. Suites are kept whole: a suite's fixtures, and the order of the tests within it, are
// unaffected.
//
// Every shard must compute the *same* partition, or suites will be run twice or not at all. The
// partition is therefore a pure function of the registered suite names (sorted, so that
// registration order and file names cannot shift it), the shard count, the seed, and the weights.
// Each shard prints a fingerprint of those inputs; a runner that collects shards MUST check that
// the fingerprints match before trusting the results.
//
// A non-zero seed randomizes which suites are co-resident. That is deliberate: co-residency is what
// exposes suites that depend on another suite's global-state side effects. Pass the same seed to
// every shard of one run, and record it - it is the only way to reproduce a failure.
//
// Each shard reports only its own tally, so a runner sums them and compares the total against an
// unsharded run.

type shardSpec struct {
	index int
	total int
}

// parseShardSpec parses the "i/n" form, with i zero-based and i < n.
func parseShardSpec(value string) (shardSpec, error) {
	slash := strings.IndexByte(value, '/')
	if slash < 0 {
		return shardSpec{}, fmt.Errorf("check: -check.shard must look like i/n, got %q", value)
	}
	index, err := strconv.Atoi(strings.TrimSpace(value[:slash]))
	if err != nil {
		return shardSpec{}, fmt.Errorf("check: bad shard index in %q: %v", value, err)
	}
	total, err := strconv.Atoi(strings.TrimSpace(value[slash+1:]))
	if err != nil {
		return shardSpec{}, fmt.Errorf("check: bad shard count in %q: %v", value, err)
	}
	if total < 1 {
		return shardSpec{}, fmt.Errorf("check: shard count must be at least 1, got %d", total)
	}
	if index < 0 || index >= total {
		return shardSpec{}, fmt.Errorf("check: shard index %d out of range for %d shards", index, total)
	}
	return shardSpec{index: index, total: total}, nil
}

// suiteName is the name a weights file and a partition identify a suite by: the bare type name, as
// gocheck itself reports it in test names and xUnit classnames.
func suiteName(suite interface{}) string {
	t := reflect.TypeOf(suite)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

// loadShardWeights reads "SuiteName<TAB>seconds" lines, ignoring blank lines and #-comments. The
// file must be identical for every shard of a run; its digest is folded into the fingerprint so
// that a mismatch is reported rather than silently splitting coverage.
//
// It is meant to be a full census of the package's suites: a suite the file does not mention is
// scheduled at the mean of the ones it does, which is only a sane estimate if the file covers
// nearly everything. That keeps a newly added suite scheduled somewhere - never dropped - and the
// count of such suites is printed alongside the fingerprint so a stale file is visible.
func loadShardWeights(path string) (map[string]float64, string, error) {
	if path == "" {
		return nil, "none", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	weights := make(map[string]float64)
	digest := sha256.New()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fmt.Fprintln(digest, line)
		name, seconds, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, "", fmt.Errorf("check: bad weights line %q (want name<TAB>seconds)", line)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(seconds), 64)
		if err != nil {
			return nil, "", fmt.Errorf("check: bad weight for %s: %v", name, err)
		}
		if value < 0 {
			return nil, "", fmt.Errorf("check: negative weight for %s: %v", name, value)
		}
		weights[strings.TrimSpace(name)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	return weights, hex.EncodeToString(digest.Sum(nil))[:12], nil
}

// shardPlan is what one shard needs to know about a partition, plus the diagnostics a collecting
// runner checks.
type shardPlan struct {
	names         []string
	fingerprint   string
	weightsDigest string
	unweighted    int
}

// partitionNames assigns every name to exactly one shard and returns this shard's share.
//
// Balance uses longest-processing-time-first: sort by descending weight, then repeatedly place the
// next suite in the least-loaded shard. With no weights every suite counts as one, which degrades
// to round-robin over the (optionally shuffled) name order.
func partitionNames(names []string, spec shardSpec, seed int64, weights map[string]float64) shardPlan {
	// Canonical order first: the partition must not depend on registration order.
	ordered := make([]string, len(names))
	copy(ordered, names)
	sort.Strings(ordered)

	plan := shardPlan{fingerprint: shardFingerprint(ordered, spec.total, seed, weights)}
	for _, name := range ordered {
		if _, ok := weights[name]; !ok {
			plan.unweighted++
		}
	}

	if seed != 0 {
		rand.New(rand.NewSource(seed)).Shuffle(len(ordered), func(i, j int) {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		})
	}

	mean := 1.0
	if len(weights) > 0 {
		var sum float64
		for _, value := range weights {
			sum += value
		}
		mean = sum / float64(len(weights))
	}
	weightOf := func(name string) float64 {
		if value, ok := weights[name]; ok {
			return value
		}
		return mean
	}

	// Stable sort on the (possibly shuffled) order above, so the seed still decides co-residency
	// among suites of equal weight.
	order := make([]int, len(ordered))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return weightOf(ordered[order[a]]) > weightOf(ordered[order[b]])
	})

	load := make([]float64, spec.total)
	for _, at := range order {
		name := ordered[at]
		least := 0
		for shard := 1; shard < spec.total; shard++ {
			if load[shard] < load[least] {
				least = shard
			}
		}
		load[least] += weightOf(name)
		if least == spec.index {
			plan.names = append(plan.names, name)
		}
	}
	return plan
}

// shardFingerprint digests everything that determines the partition. Shards whose fingerprints
// differ did not agree on the split, so between them some suites ran twice and others not at all.
func shardFingerprint(sortedNames []string, total int, seed int64, weights map[string]float64) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1\nshards=%d\nseed=%d\nsuites=%d\n", total, seed, len(sortedNames))
	for _, name := range sortedNames {
		fmt.Fprintf(h, "%s\t%g\n", name, weights[name])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// shardOutputPath gives a shard its own report file. Without it every shard of a package expands
// the same %pkg token and they all truncate one another's report.
func shardOutputPath(filename string, spec shardSpec) string {
	ext := filepath.Ext(filename)
	return fmt.Sprintf("%s.shard%dof%d%s", strings.TrimSuffix(filename, ext), spec.index, spec.total, ext)
}

// shardSuites narrows the registered suites to the shard named by runConf, or returns them all when
// sharding is off. It reports the plan so the caller can announce it.
func shardSuites(runConf *RunConf) ([]s, *shardPlan, error) {
	if runConf.Shard == "" {
		return allSuites, nil, nil
	}
	spec, err := parseShardSpec(runConf.Shard)
	if err != nil {
		return nil, nil, err
	}
	weights, weightsDigest, err := loadShardWeights(runConf.ShardWeights)
	if err != nil {
		return nil, nil, err
	}

	names := make([]string, 0, len(allSuites))
	byName := make(map[string]s, len(allSuites))
	for _, suite := range allSuites {
		name := suiteName(suite.suite)
		names = append(names, name)
		byName[name] = suite
	}

	plan := partitionNames(names, spec, runConf.ShardSeed, weights)
	mine := make([]s, 0, len(plan.names))
	for _, name := range plan.names {
		mine = append(mine, byName[name])
	}
	plan.weightsDigest = weightsDigest
	return mine, &plan, nil
}

// announceShard prints the line a collecting runner parses to confirm the shards agreed.
func announceShard(w io.Writer, runConf *RunConf, plan *shardPlan, registered int) {
	fmt.Fprintf(w, "check: shard %s seed=%d suites=%d/%d unweighted=%d weights=%s partition=%s\n",
		runConf.Shard, runConf.ShardSeed, len(plan.names), registered, plan.unweighted,
		plan.weightsDigest, plan.fingerprint)
}
