// These tests cover the report routing helpers in run.go: how -check.output
// is resolved to a per package file and how -check.r is parsed.

package check

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
)

type OutputPathTestSuite struct{}

var _ = Suite(&OutputPathTestSuite{})

func (s *OutputPathTestSuite) TestFuncNamePackagePath(c *C) {
	for _, t := range []struct {
		name     string
		expected string
	}{
		{"example.com/project/timings.Test", "example.com/project/timings"},
		{"example.com/project/api.(*TasksTests).TestFoo", "example.com/project/api"},
		{"example.com/project/api.TasksTests.TestFoo", "example.com/project/api"},
		{"main.Test", "main"},
		{"example.com/x/v2.Test", "example.com/x/v2"},
		{"example.com/x.Test.func1", "example.com/x"},
		{"nodots", "nodots"},
		// The linker escapes a dot in the final path element as %2e so that
		// the package/function boundary stays unambiguous.
		{"gopkg.in/check%2ev1.TestingT", "gopkg.in/check.v1"},
		{"gopkg.in/check%2ev1.(*C).Assert", "gopkg.in/check.v1"},
		{"example%2eorg.Test", "example.org"},
	} {
		c.Check(funcNamePackagePath(t.name), Equals, t.expected,
			Commentf("input %q", t.name))
	}
}

func (s *OutputPathTestSuite) TestGetFuncPackagePathOfThisSuite(c *C) {
	// reflect knows the real import path, whatever this fork is built under,
	// so compare against that rather than a hardcoded name.
	c.Assert(getFuncPackagePath(c.method.PC()), Equals,
		reflect.TypeOf(s).Elem().PkgPath())
}

func (s *OutputPathTestSuite) TestPackageFileToken(c *C) {
	c.Check(packageFileToken("example.com/project/timings"), Equals, "example.com_project_timings")
	c.Check(packageFileToken("example.com/project/api"), Equals, "example.com_project_api")
	c.Check(packageFileToken("main"), Equals, "main")
	c.Check(packageFileToken(""), Equals, "unknown_package")
}

func (s *OutputPathTestSuite) TestExpandOutputPath(c *C) {
	c.Check(expandOutputPath("junit-check-%pkg.xml", "example.com/project/timings"),
		Equals, "junit-check-example.com_project_timings.xml")
	c.Check(expandOutputPath("reports/%pkg/%pkg.xml", "a/b"),
		Equals, "reports/a_b/a_b.xml")
	// Without the token the value is used verbatim, as it always was.
	c.Check(expandOutputPath("junit.xml", "example.com/project/timings"),
		Equals, "junit.xml")
}

func (s *OutputPathTestSuite) TestGetOutputDefaultsToStdout(c *C) {
	out, err := getOutput("", "example.com/project/timings")
	c.Assert(err, IsNil)
	c.Assert(out, Equals, os.Stdout)
}

func (s *OutputPathTestSuite) TestGetOutputCreatesPerPackageFile(c *C) {
	dir := c.MkDir()
	pattern := filepath.Join(dir, "reports", "junit", "junit-check-%pkg.xml")

	// Two packages sharing one -check.output value, which is what go test
	// hands to every package binary, must land in different files.
	for _, pkg := range []string{"example.com/project/timings", "example.com/project/api"} {
		out, err := getOutput(pattern, pkg)
		c.Assert(err, IsNil)
		f, ok := out.(*os.File)
		c.Assert(ok, Equals, true)
		_, err = f.Write([]byte(pkg))
		c.Assert(err, IsNil)
		c.Assert(f.Close(), IsNil)
	}

	for _, t := range []struct{ file, content string }{
		{"junit-check-example.com_project_timings.xml", "example.com/project/timings"},
		{"junit-check-example.com_project_api.xml", "example.com/project/api"},
	} {
		got, err := os.ReadFile(filepath.Join(dir, "reports", "junit", t.file))
		c.Assert(err, IsNil)
		c.Assert(string(got), Equals, t.content)
	}
}

func (s *OutputPathTestSuite) TestGetOutputCreatesMissingDirectories(c *C) {
	path := filepath.Join(c.MkDir(), "does", "not", "exist", "junit.xml")
	out, err := getOutput(path, "example.com/project/timings")
	c.Assert(err, IsNil)
	c.Assert(out.(*os.File).Close(), IsNil)
	_, err = os.Stat(path)
	c.Assert(err, IsNil)
}

func (s *OutputPathTestSuite) TestCallerPackagePath(c *C) {
	c.Assert(callerPackagePath(0), Equals, reflect.TypeOf(s).Elem().PkgPath())
}

type ReporterFlagTestSuite struct{}

var _ = Suite(&ReporterFlagTestSuite{})

func (s *ReporterFlagTestSuite) TestParseSingle(c *C) {
	names, err := parseReporters("xunit")
	c.Assert(err, IsNil)
	c.Assert(names, DeepEquals, []string{"xunit"})
}

func (s *ReporterFlagTestSuite) TestParseListIgnoresSurroundingSpace(c *C) {
	names, err := parseReporters(" plain , xunit ")
	c.Assert(err, IsNil)
	c.Assert(names, DeepEquals, []string{"plain", "xunit"})
}

func (s *ReporterFlagTestSuite) TestParseRejectsEmptyEntry(c *C) {
	_, err := parseReporters("plain,")
	c.Assert(err, ErrorMatches, "empty reporter name provided in: plain,")
	_, err = parseReporters("")
	c.Assert(err, ErrorMatches, "empty reporter name provided in: ")
}

func (s *ReporterFlagTestSuite) TestParseRejectsDuplicates(c *C) {
	_, err := parseReporters("xunit,xunit")
	c.Assert(err, ErrorMatches, "duplicate reporter name provided: xunit")
}

func (s *ReporterFlagTestSuite) TestGetWriterRejectsUnknownName(c *C) {
	_, err := getWriter("nope", os.Stdout, false, false)
	c.Assert(err, ErrorMatches, "unknown reporter name provided: nope")
}

type FlagEnvTestSuite struct{}

var _ = Suite(&FlagEnvTestSuite{})

func (s *FlagEnvTestSuite) TestEnvSuppliesTheDefault(c *C) {
	c.Assert(resolveFlag(false, "plain", "plain,xunit"), Equals, "plain,xunit")
	c.Assert(resolveFlag(false, "", "reports/j-%pkg.xml"), Equals, "reports/j-%pkg.xml")
}

// An explicit flag has to beat the environment, so that a local run can
// override whatever the CI harness exported.
func (s *FlagEnvTestSuite) TestPassedFlagBeatsEnv(c *C) {
	c.Assert(resolveFlag(true, "plain", "plain,xunit"), Equals, "plain")
	c.Assert(resolveFlag(true, "/tmp/foo.xml", "reports/j-%pkg.xml"), Equals, "/tmp/foo.xml")
}

// An unset or empty variable must leave the flag default alone rather than
// reaching parseReporters as a malformed value.
func (s *FlagEnvTestSuite) TestEmptyEnvIsIgnored(c *C) {
	c.Assert(resolveFlag(false, "plain", ""), Equals, "plain")
	c.Assert(resolveFlag(false, "", ""), Equals, "")
}

func (s *FlagEnvTestSuite) TestFlagWasSet(c *C) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("check.r", "plain", "")
	fs.String("check.output", "", "")

	c.Assert(flagWasSet(fs, "check.r"), Equals, false)

	// Explicitly passing the flag's own default value still counts as passed,
	// which is the case the value alone cannot detect.
	c.Assert(fs.Parse([]string{"-check.r=plain"}), IsNil)
	c.Assert(flagWasSet(fs, "check.r"), Equals, true)
	c.Assert(flagWasSet(fs, "check.output"), Equals, false)
}

func (s *FlagEnvTestSuite) TestFlagOrEnvReadsTheEnvironment(c *C) {
	const name = "GOCHECK_TEST_ONLY_VAR"
	c.Assert(os.Setenv(name, "from-env"), IsNil)
	defer os.Unsetenv(name)

	// Name a flag that cannot have been passed, so the result does not depend
	// on how this suite itself was invoked; flagWasSet is covered separately.
	const unpassed = "gocheck.no.such.flag"
	c.Assert(flagOrEnv(unpassed, name, "plain"), Equals, "from-env")
	c.Assert(flagOrEnv(unpassed, "GOCHECK_TEST_ONLY_UNSET", "plain"), Equals, "plain")
}
