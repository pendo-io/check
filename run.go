package check

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// -----------------------------------------------------------------------
// Test suite registry.

type s struct {
	suite      interface{}
	concurrent bool
}

var allSuites []s

// Suite registers the given value as a test suite to be run. Any methods
// starting with the Test prefix in the given value will be considered as
// a test method.
func Suite(suite interface{}) interface{} {
	allSuites = append(allSuites, s{suite, false})
	return suite
}

func ConcurrentSuite(suite interface{}) interface{} {
	allSuites = append(allSuites, s{suite, true})
	return suite
}

// -----------------------------------------------------------------------
// Public running interface.

var (
	oldFilterFlag  = flag.String("gocheck.f", "", "Regular expression selecting which tests and/or suites to run")
	oldVerboseFlag = flag.Bool("gocheck.v", false, "Verbose mode")
	oldStreamFlag  = flag.Bool("gocheck.vv", false, "Super verbose mode (disables output caching)")
	oldBenchFlag   = flag.Bool("gocheck.b", false, "Run benchmarks")
	oldBenchTime   = flag.Duration("gocheck.btime", 1*time.Second, "approximate run time for each benchmark")
	oldListFlag    = flag.Bool("gocheck.list", false, "List the names of all tests that will be run")
	oldWorkFlag    = flag.Bool("gocheck.work", false, "Display and do not remove the test working directory")

	newFilterFlag      = flag.String("check.f", "", "Regular expression selecting which tests and/or suites to run")
	newVerboseFlag     = flag.Bool("check.v", false, "Verbose mode")
	newStreamFlag      = flag.Bool("check.vv", false, "Super verbose mode (disables output caching)")
	newBenchFlag       = flag.Bool("check.b", false, "Run benchmarks")
	newBenchTime       = flag.Duration("check.btime", 1*time.Second, "approximate run time for each benchmark")
	newBenchMem        = flag.Bool("check.bmem", false, "Report memory benchmarks")
	newListFlag        = flag.Bool("check.list", false, "List the names of all tests that will be run")
	newWorkFlag        = flag.Bool("check.work", false, "Display and do not remove the test working directory")
	reporterFlag       = flag.String("check.r", "plain", "Comma separated list of reporters for outputting results: [plain|xunit]. Defaults to $GOCHECK_REPORTERS when not given")
	outputFlag         = flag.String("check.output", "", "Name of the file to print report into. The token %pkg is replaced by the import path of the package under test, with slashes turned into underscores, so that packages tested in parallel do not overwrite each other. Defaults to $GOCHECK_OUTPUT when not given; if both are empty, stdout is used")
	newConcurrencyFlag = flag.Int("check.c", 5, "How many tests to run concurrently for concurrent test suites")
)

// TestingT runs all test suites registered with the Suite function,
// printing results to stdout, and reporting any failures back to
// the "testing" package.
func TestingT(testingT *testing.T) {
	benchTime := *newBenchTime
	if benchTime == 1*time.Second {
		benchTime = *oldBenchTime
	}
	conf := &RunConf{
		Filter:           *oldFilterFlag + *newFilterFlag,
		Verbose:          *oldVerboseFlag || *newVerboseFlag,
		Stream:           *oldStreamFlag || *newStreamFlag,
		Benchmark:        *oldBenchFlag || *newBenchFlag,
		BenchmarkTime:    benchTime,
		BenchmarkMem:     *newBenchMem,
		KeepWorkDir:      *oldWorkFlag || *newWorkFlag,
		ConcurrencyLevel: *newConcurrencyFlag,
	}
	names, err := parseReporters(flagOrEnv("check.r", envReporters, *reporterFlag))
	if err != nil {
		testingT.Fatal(err.Error())
	}

	// Listing does not run anything, so avoid creating an output file for it.
	if *oldListFlag || *newListFlag {
		conf.Output = os.Stdout
		w := bufio.NewWriter(os.Stdout)
		for _, name := range ListAll(conf) {
			fmt.Fprintln(w, name)
		}
		w.Flush()
		return
	}

	// %pkg resolves against the package that called TestingT, which is the
	// package whose tests are about to run.
	fileOutput, err := getOutput(flagOrEnv("check.output", envOutput, *outputFlag),
		callerPackagePath(1))
	if err != nil {
		testingT.Fatal(err.Error())
	}

	// With a single reporter everything goes to -check.output, as it always
	// has. With several, the machine readable reports take the file and the
	// human readable log keeps the console, so that a CI failure still prints
	// something useful.
	logOutput, reportOutput := fileOutput, fileOutput
	if len(names) > 1 {
		logOutput = os.Stdout
	}
	conf.Output = logOutput

	writers := make([]outputWriter, 0, len(names))
	for _, name := range names {
		// A reporter that produces a report writes it to reportOutput at the
		// end; its log writer is only used in stream mode, and only the plain
		// reporter should be narrating to the console.
		logTo := logOutput
		if len(names) > 1 && name != "plain" {
			logTo = nil
		}
		w, err := getWriter(name, logTo, conf.Verbose, conf.Stream)
		if err != nil {
			testingT.Fatal(err.Error())
		}
		writers = append(writers, w)
	}
	conf.Writer = combineWriters(writers)

	result := RunAll(conf)

	reporting := 0
	for _, w := range writers {
		reporter, ok := w.(reporter)
		if !ok {
			continue
		}
		report, err := reporter.GetReport()
		if err != nil {
			testingT.Fatalf("could not generate report: %s", err.Error())
		}
		fmt.Fprintf(reportOutput, "%s", string(report))
		reporting++
	}
	// Reporters such as plain have no report of their own; as long as one of
	// them is active the familiar one line summary is still printed.
	if reporting < len(writers) {
		fmt.Fprintf(logOutput, "%s\n", result.String())
	}

	if !result.Passed() {
		testingT.Fail()
	}
}

// Environment variables supplying defaults for the reporting flags. Passing
// -check.r or -check.output to "go test ./..." fails outright in any package
// that does not link gocheck, with "flag provided but not defined", so a
// repository mixing gocheck and plain testing packages has to configure the
// reporters out of band.
const (
	envReporters = "GOCHECK_REPORTERS"
	envOutput    = "GOCHECK_OUTPUT"
)

// flagOrEnv returns the value to use for a flag, preferring the environment
// variable named by envName over the flag's default.
func flagOrEnv(flagName, envName, flagValue string) string {
	return resolveFlag(flagWasSet(flag.CommandLine, flagName), flagValue, os.Getenv(envName))
}

// resolveFlag decides between a flag value and an environment override. A
// flag that was actually passed always wins, so a local
// -check.output=/tmp/foo.xml still beats whatever the CI harness exported. An
// unset or empty environment variable leaves the flag's own default in place,
// rather than being reported as a malformed value.
func resolveFlag(passed bool, flagValue, envValue string) string {
	if passed || envValue == "" {
		return flagValue
	}
	return envValue
}

// flagWasSet reports whether the named flag was given on the command line, as
// opposed to holding its default. -check.r defaults to "plain", so its value
// alone cannot distinguish an explicit -check.r=plain from an absent flag.
func flagWasSet(fs *flag.FlagSet, name string) (passed bool) {
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			passed = true
		}
	})
	return
}

// parseReporters splits the -check.r value into reporter names, rejecting
// empty entries and duplicates.
func parseReporters(value string) ([]string, error) {
	var names []string
	seen := make(map[string]bool)
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("empty reporter name provided in: " + value)
		}
		if seen[name] {
			return nil, errors.New("duplicate reporter name provided: " + name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// callerPackagePath returns the import path of the package containing the
// function skip levels above the caller of this function.
func callerPackagePath(skip int) string {
	pc, _, _, ok := runtime.Caller(skip + 1)
	if !ok {
		return ""
	}
	return getFuncPackagePath(pc)
}

// packageFileToken renders an import path as a single file name component.
func packageFileToken(pkg string) string {
	if pkg == "" {
		return "unknown_package"
	}
	return strings.NewReplacer("/", "_", "\\", "_").Replace(pkg)
}

// expandOutputPath substitutes the %pkg token in an -check.output value.
func expandOutputPath(filename, pkg string) string {
	return strings.Replace(filename, "%pkg", packageFileToken(pkg), -1)
}

func getOutput(filename string, pkg string) (io.Writer, error) {
	if filename == "" {
		return os.Stdout, nil
	}
	filename = expandOutputPath(filename, pkg)
	// go test runs package binaries in parallel from arbitrary working
	// directories, so the report directory may not exist yet.
	if dir := filepath.Dir(filename); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}
	return os.Create(filename)
}

// factory method that returns instance of reporter by name
func getWriter(name string, writer io.Writer, verbose, stream bool) (outputWriter, error) {
	switch name {
	case "plain":
		return newPlainWriter(writer, verbose, stream), nil
	case "xunit":
		return newXunitWriter(writer, stream), nil
	default:
		return nil, errors.New("unknown reporter name provided: " + name)
	}
}

// RunAll runs all test suites registered with the Suite function, using the
// provided run configuration.
func RunAll(runConf *RunConf) *Result {
	concurrent := make([]interface{}, 0, len(allSuites))
	serial := make([]interface{}, 0, len(allSuites))
	for _, s := range allSuites {
		if s.concurrent {
			concurrent = append(concurrent, s.suite)
		} else {
			serial = append(serial, s.suite)
		}
	}
	result := Result{}
	if len(concurrent) > 0 {
		bucket := newConcurrencyBucket(runConf.ConcurrencyLevel)
		var mtx sync.Mutex
		var wg sync.WaitGroup
		wg.Add(len(concurrent))
		for _, suite := range concurrent {
			go func(suite interface{}) {
				r := RunConcurrent(suite, runConf, bucket)
				mtx.Lock()
				result.Add(r)
				mtx.Unlock()
				wg.Done()
			}(suite)
		}
		wg.Wait()
		bucket.drain()
	}
	for _, suite := range serial {
		result.Add(Run(suite, runConf))
	}
	return &result
}

// Run runs the provided test suite using the provided run configuration.
func Run(suite interface{}, runConf *RunConf) *Result {
	runner := newSuiteRunner(suite, runConf, false, nil)
	return runner.run()
}

// RunConcurrent runs the provided test suite concurrently using the provided run configuration.
func RunConcurrent(suite interface{}, runConf *RunConf, bucket *concurrencyBucket) *Result {
	runner := newSuiteRunner(suite, runConf, true, bucket)
	return runner.run()
}

// ListAll returns the names of all the test functions registered with the
// Suite function that will be run with the provided run configuration.
func ListAll(runConf *RunConf) []string {
	var names []string
	for _, suite := range allSuites {
		names = append(names, List(suite, runConf)...)
	}
	return names
}

// List returns the names of the test functions in the given
// suite that will be run with the provided run configuration.
func List(suite interface{}, runConf *RunConf) []string {
	var names []string
	runner := newSuiteRunner(suite, runConf, false, nil)
	for _, t := range runner.tests {
		names = append(names, t.String())
	}
	return names
}

// -----------------------------------------------------------------------
// Result methods.

func (r *Result) Add(other *Result) {
	r.Succeeded += other.Succeeded
	r.Skipped += other.Skipped
	r.Failed += other.Failed
	r.Panicked += other.Panicked
	r.FixturePanicked += other.FixturePanicked
	r.ExpectedFailures += other.ExpectedFailures
	r.Missed += other.Missed
	if r.WorkDir != "" && other.WorkDir != "" {
		r.WorkDir += ":" + other.WorkDir
	} else if other.WorkDir != "" {
		r.WorkDir = other.WorkDir
	}
}

func (r *Result) Passed() bool {
	return (r.Failed == 0 && r.Panicked == 0 &&
		r.FixturePanicked == 0 && r.Missed == 0 &&
		r.RunError == nil)
}

func (r *Result) String() string {
	if r.RunError != nil {
		return "ERROR: " + r.RunError.Error()
	}

	var value string
	if r.Failed == 0 && r.Panicked == 0 && r.FixturePanicked == 0 &&
		r.Missed == 0 {
		value = "OK: "
	} else {
		value = "OOPS: "
	}
	value += fmt.Sprintf("%d passed", r.Succeeded)
	if r.Skipped != 0 {
		value += fmt.Sprintf(", %d skipped", r.Skipped)
	}
	if r.ExpectedFailures != 0 {
		value += fmt.Sprintf(", %d expected failures", r.ExpectedFailures)
	}
	if r.Failed != 0 {
		value += fmt.Sprintf(", %d FAILED", r.Failed)
	}
	if r.Panicked != 0 {
		value += fmt.Sprintf(", %d PANICKED", r.Panicked)
	}
	if r.FixturePanicked != 0 {
		value += fmt.Sprintf(", %d FIXTURE-PANICKED", r.FixturePanicked)
	}
	if r.Missed != 0 {
		value += fmt.Sprintf(", %d MISSED", r.Missed)
	}
	if r.WorkDir != "" {
		value += "\nWORK=" + r.WorkDir
	}
	return value
}
