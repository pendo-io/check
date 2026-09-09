package check

import (
	"bytes"
	"strings"
)

/*************** xUnit writer tests *****************/
type XUnitTestSuite struct {
	writer *xunitWriter
}

var _ = Suite(&XUnitTestSuite{})

func (s *XUnitTestSuite) SetUpTest(c *C) {
	s.writer = newXunitWriter(nil, false)
}

func (s *XUnitTestSuite) TestSuccess(c *C) {
	s.writer.WriteCallSuccess("PASS", c)
	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	match := "<testsuites>\n" +
		" +<testsuite .*name=\"XUnitTestSuite\" .*tests=\"1\" failures=\"0\" errors=\"0\" skipped=\"0\">\n" +
		" +<testcase name=\"XUnitTestSuite\\.TestSuccess\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*</testcase>\n" +
		" +</testsuite>\n" +
		"</testsuites>"

	c.Assert(string(report), Matches, match)
}

func (s *XUnitTestSuite) TestSkip(c *C) {
	s.writer.WriteCallSkipped("SKIP", c)
	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	match := "<testsuites>\n" +
		" +<testsuite .*name=\"XUnitTestSuite\" .*tests=\"1\" failures=\"0\" errors=\"0\" skipped=\"1\">\n" +
		" +<testcase name=\"XUnitTestSuite\\.TestSkip\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<skipped>true</skipped>\n" +
		" +</testcase>\n" +
		" +</testsuite>\n" +
		"</testsuites>"

	c.Assert(string(report), Matches, match)
}

func (s *XUnitTestSuite) TestFail(c *C) {
	s.writer.WriteCallFailure("FAIL", c)
	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	match := "<testsuites>\n" +
		" +<testsuite .*name=\"XUnitTestSuite\" .*tests=\"1\" failures=\"1\" errors=\"0\" skipped=\"0\">\n" +
		" +<testcase name=\"XUnitTestSuite\\.TestFail\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<failure message=\"FAIL\" type=\"go.failure\"></failure>\n" +
		" +</testcase>\n" +
		" +</testsuite>\n" +
		"</testsuites>"

	c.Assert(string(report), Matches, match)
}

func (s *XUnitTestSuite) TestError(c *C) {
	s.writer.WriteCallError("ERR", c)
	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	match := "<testsuites>\n" +
		" +<testsuite .*name=\"XUnitTestSuite\" .*tests=\"1\" failures=\"0\" errors=\"1\" skipped=\"0\">\n" +
		" +<testcase name=\"XUnitTestSuite\\.TestError\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<error message=\"ERR\" type=\"go.error\"></error>\n" +
		" +</testcase>\n" +
		" +</testsuite>\n" +
		"</testsuites>"

	c.Assert(string(report), Matches, match)

}

func (s *XUnitTestSuite) TestCombine(c *C) {
	s.writer.WriteCallError("ERR", c)
	s.writer.WriteCallFailure("FAIL", c)
	s.writer.WriteCallSuccess("PASS", c)
	s.writer.WriteCallSuccess("PASS", c)
	s.writer.WriteCallFailure("FAIL", c)
	s.writer.WriteCallSkipped("SKIP", c)

	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	match := "<testsuites>\n" +
		" +<testsuite .*name=\"XUnitTestSuite\" .*tests=\"6\" failures=\"2\" errors=\"1\" skipped=\"1\">\n" +
		" +<testcase name=\"XUnitTestSuite\\.TestCombine\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<error message=\"ERR\" type=\"go.error\"></error>\n" +
		" +</testcase>\n" +

		" +<testcase name=\"XUnitTestSuite\\.TestCombine\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<failure message=\"FAIL\" type=\"go.failure\"></failure>\n" +
		" +</testcase>\n" +

		" +<testcase name=\"XUnitTestSuite\\.TestCombine\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*</testcase>\n" +

		" +<testcase name=\"XUnitTestSuite\\.TestCombine\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*</testcase>\n" +

		" +<testcase name=\"XUnitTestSuite\\.TestCombine\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<failure message=\"FAIL\" type=\"go.failure\"></failure>\n" +
		" +</testcase>\n" +

		" +<testcase name=\"XUnitTestSuite\\.TestCombine\" classname=\"[^\"]*XUnitTestSuite\" .*file=\"[^\"]*reporter_test.go\".*>\n" +
		" +<skipped>true</skipped>\n" +
		" +</testcase>\n" +

		" +</testsuite>\n" +
		"</testsuites>"

	c.Assert(string(report), Matches, match)
}

func (s *XUnitTestSuite) TestClassnameIsPackageQualified(c *C) {
	s.writer.WriteCallSuccess("PASS", c)
	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	// The package part is whatever import path this fork is built under, so
	// assert on the shape rather than on a fixed prefix.
	pkg := getFuncPackagePath(c.method.PC())
	c.Assert(pkg, Not(Equals), "")
	c.Assert(strings.Contains(string(report),
		`classname="`+pkg+`.XUnitTestSuite"`), Equals, true,
		Commentf("report was:\n%s", report))
}

// CircleCI groups by the testsuite package attribute, so it has to be the
// full import path; the bare leaf name is identical for, say, a/util and
// b/util.
func (s *XUnitTestSuite) TestSuitePackageIsFullImportPath(c *C) {
	s.writer.WriteCallSuccess("PASS", c)
	report, err := s.writer.GetReport()
	c.Assert(err, IsNil)

	pkg := getFuncPackagePath(c.method.PC())
	c.Assert(pkg, Not(Equals), "")
	c.Assert(strings.Contains(string(report), `package="`+pkg+`"`), Equals, true,
		Commentf("report was:\n%s", report))
}

/*************** Multi writer tests *****************/

// countingWriter records which callbacks it received.
type countingWriter struct {
	outputWriter
	started, success, skipped, failure, errored int
	written                                     []string
	stream                                      bool
}

func (w *countingWriter) StreamEnabled() bool { return w.stream }
func (w *countingWriter) Write(content []byte) (int, error) {
	w.written = append(w.written, string(content))
	return len(content), nil
}
func (w *countingWriter) WriteCallStarted(label string, c *C) { w.started++ }
func (w *countingWriter) WriteCallSuccess(label string, c *C) { w.success++ }
func (w *countingWriter) WriteCallSkipped(label string, c *C) { w.skipped++ }
func (w *countingWriter) WriteCallFailure(label string, c *C) { w.failure++ }
func (w *countingWriter) WriteCallError(label string, c *C)   { w.errored++ }

type MultiWriterTestSuite struct{}

var _ = Suite(&MultiWriterTestSuite{})

func (s *MultiWriterTestSuite) TestSingleWriterIsNotWrapped(c *C) {
	sub := &countingWriter{}
	c.Assert(combineWriters([]outputWriter{sub}), Equals, outputWriter(sub))
}

func (s *MultiWriterTestSuite) TestFansOutEveryCallback(c *C) {
	a, b := &countingWriter{}, &countingWriter{}
	w := combineWriters([]outputWriter{a, b})

	w.WriteCallStarted("START", c)
	w.WriteCallSuccess("PASS", c)
	w.WriteCallSkipped("SKIP", c)
	w.WriteCallFailure("FAIL", c)
	w.WriteCallError("PANIC", c)

	for _, sub := range []*countingWriter{a, b} {
		c.Check(sub.started, Equals, 1)
		c.Check(sub.success, Equals, 1)
		c.Check(sub.skipped, Equals, 1)
		c.Check(sub.failure, Equals, 1)
		c.Check(sub.errored, Equals, 1)
	}
}

func (s *MultiWriterTestSuite) TestWriteReachesEveryWriter(c *C) {
	a, b := &countingWriter{}, &countingWriter{}
	w := combineWriters([]outputWriter{a, b})

	n, err := w.Write([]byte("hello"))
	c.Assert(err, IsNil)
	c.Assert(n, Equals, 5)
	c.Assert(a.written, DeepEquals, []string{"hello"})
	c.Assert(b.written, DeepEquals, []string{"hello"})
}

func (s *MultiWriterTestSuite) TestStreamEnabledIfAnyWriterStreams(c *C) {
	c.Assert(combineWriters([]outputWriter{
		&countingWriter{}, &countingWriter{}}).StreamEnabled(), Equals, false)
	c.Assert(combineWriters([]outputWriter{
		&countingWriter{}, &countingWriter{stream: true}}).StreamEnabled(), Equals, true)
}

// A nil log writer is how run.go silences the non-plain reporters when
// several are combined, so it must not panic.
func (s *MultiWriterTestSuite) TestXunitWriterToleratesNilLogWriter(c *C) {
	w := newXunitWriter(nil, false)
	n, err := w.Write([]byte("ignored"))
	c.Assert(err, IsNil)
	c.Assert(n, Equals, 0)
}

// Combining reporters must not let the first one consume the failure log out
// from under the second.
func (s *MultiWriterTestSuite) TestFailureBodySurvivesBothReporters(c *C) {
	var console bytes.Buffer
	plain := newPlainWriter(&console, false, false)
	xunit := newXunitWriter(nil, false)
	w := combineWriters([]outputWriter{plain, xunit})

	c.logb.Write([]byte("the failure detail"))
	w.WriteCallFailure("FAIL", c)

	c.Check(strings.Contains(console.String(), "the failure detail"), Equals, true,
		Commentf("console was:\n%s", console.String()))

	report, err := xunit.GetReport()
	c.Assert(err, IsNil)
	c.Check(strings.Contains(string(report), "the failure detail"), Equals, true,
		Commentf("report was:\n%s", report))
}
