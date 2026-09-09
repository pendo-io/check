package check

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type reporter interface {
	GetReport() ([]byte, error)
}

type outputWriter interface {
	Write(content []byte) (n int, err error)
	WriteCallStarted(label string, c *C)

	WriteCallSuccess(label string, c *C)
	WriteCallSkipped(label string, c *C)

	WriteCallError(label string, c *C)
	WriteCallFailure(label string, c *C)
	StreamEnabled() bool
}

/*************** Plain writer *****************/

type plainWriter struct {
	outputWriter
	m                    sync.Mutex
	writer               io.Writer
	wroteCallProblemLast bool
	stream               bool
	verbose              bool
}

func newPlainWriter(writer io.Writer, verbose, stream bool) *plainWriter {
	return &plainWriter{writer: writer, stream: stream, verbose: verbose}
}

func (w *plainWriter) StreamEnabled() bool { return w.stream }

func (w *plainWriter) Write(content []byte) (n int, err error) {
	w.m.Lock()
	n, err = w.writer.Write(content)
	w.m.Unlock()
	return
}

func (w *plainWriter) WriteCallStarted(label string, c *C) {
	if w.stream {
		header := renderCallHeader(label, c, "", "\n")
		w.m.Lock()
		w.writer.Write([]byte(header))
		w.m.Unlock()
	}
}

func (w *plainWriter) WriteCallSkipped(label string, c *C) {
	w.writeSuccess(label, c)
}

func (w *plainWriter) WriteCallFailure(label string, c *C) {
	w.writeProblem(label, c)
}

func (w *plainWriter) WriteCallError(label string, c *C) {
	w.writeProblem(label, c)
}

func (w *plainWriter) WriteCallSuccess(label string, c *C) {
	w.writeSuccess(label, c)
}

func (w *plainWriter) writeProblem(label string, c *C) {
	var prefix string
	if !w.stream {
		prefix = "\n-----------------------------------" +
			"-----------------------------------\n"
	}
	header := renderCallHeader(label, c, prefix, "\n\n")
	w.m.Lock()
	w.wroteCallProblemLast = true
	w.writer.Write([]byte(header))
	if !w.stream {
		c.logb.WriteTo(w.writer)
	}
	w.m.Unlock()
}

func (w *plainWriter) writeSuccess(label string, c *C) {
	if w.stream || (w.verbose && c.kind == testKd) {
		// TODO Use a buffer here.
		var suffix string
		if c.reason != "" {
			suffix = " (" + c.reason + ")"
		}
		if c.status == succeededSt {
			suffix += "\t" + c.timerString()
		}
		suffix += "\n"
		if w.stream {
			suffix += "\n"
		}
		header := renderCallHeader(label, c, "", suffix)
		w.m.Lock()
		// Resist temptation of using line as prefix above due to race.
		if !w.stream && w.wroteCallProblemLast {
			header = "\n-----------------------------------" +
				"-----------------------------------\n" +
				header
		}
		w.wroteCallProblemLast = false
		w.writer.Write([]byte(header))
		w.m.Unlock()
	}
}

func renderCallHeader(label string, c *C, prefix, suffix string) string {
	pc := c.method.PC()
	return fmt.Sprintf("%s%s: %s: %s%s", prefix, label, niceFuncPath(pc),
		niceFuncName(pc), suffix)
}

/*************** Multi writer *****************/

// multiWriter fans every event out to several reporters at once, so that a
// run can produce a human readable log and a machine readable report at the
// same time.
type multiWriter struct {
	writers []outputWriter
}

// combineWriters returns a single outputWriter driving all of writers. A lone
// writer is returned as is, so the common single reporter case stays exactly
// as it was.
//
// Writers are reordered so that the report building ones run first. They read
// the failure log without consuming it, whereas the plain writer drains it
// (see logger.WriteTo); running the plain writer first would leave every
// failure in the report with an empty body.
func combineWriters(writers []outputWriter) outputWriter {
	if len(writers) == 1 {
		return writers[0]
	}
	ordered := make([]outputWriter, 0, len(writers))
	for _, w := range writers {
		if _, ok := w.(reporter); ok {
			ordered = append(ordered, w)
		}
	}
	for _, w := range writers {
		if _, ok := w.(reporter); !ok {
			ordered = append(ordered, w)
		}
	}
	return &multiWriter{writers: ordered}
}

func (w *multiWriter) StreamEnabled() bool {
	for _, sub := range w.writers {
		if sub.StreamEnabled() {
			return true
		}
	}
	return false
}

func (w *multiWriter) Write(content []byte) (n int, err error) {
	for _, sub := range w.writers {
		if _, err = sub.Write(content); err != nil {
			return 0, err
		}
	}
	return len(content), nil
}

func (w *multiWriter) WriteCallStarted(label string, c *C) {
	for _, sub := range w.writers {
		sub.WriteCallStarted(label, c)
	}
}

func (w *multiWriter) WriteCallSuccess(label string, c *C) {
	for _, sub := range w.writers {
		sub.WriteCallSuccess(label, c)
	}
}

func (w *multiWriter) WriteCallSkipped(label string, c *C) {
	for _, sub := range w.writers {
		sub.WriteCallSkipped(label, c)
	}
}

func (w *multiWriter) WriteCallError(label string, c *C) {
	for _, sub := range w.writers {
		sub.WriteCallError(label, c)
	}
}

func (w *multiWriter) WriteCallFailure(label string, c *C) {
	for _, sub := range w.writers {
		sub.WriteCallFailure(label, c)
	}
}

/*************** xUnit writer *****************/
type xunitReport struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []xunitSuite `xml:"testsuite,omitempty"`
}

type xunitSuite struct {
	Package   string    `xml:"package,attr,omitempty"`
	Name      string    `xml:"name,attr,omitempty"`
	Classname string    `xml:"classname,attr,omitempty"`
	Time      float64   `xml:"time,attr"`
	Timestamp time.Time `xml:"timestamp,attr"`

	Tests    uint64 `xml:"tests,attr"`
	Failures uint64 `xml:"failures,attr"`
	Errors   uint64 `xml:"errors,attr"`
	Skipped  uint64 `xml:"skipped,attr"`

	// TODO: according specs suite also contains Properties node
	// but reporter has no use for it for now
	Testcases []xunitTestcase `xml:"testcase,omitempty"`

	// TODO: specs define also nodes "properties", "system-out" and "system-err"
	// but reporter has no use for them for now

	m sync.Mutex
}

func (s *xunitSuite) TestFail(tc xunitTestcase, message, value string) {
	tc.Failure = &xunitTestcaseResult{
		Message: message,
		Value:   value,
		Type:    "go.failure",
	}

	s.m.Lock()
	s.Failures++
	s.addTestCase(tc)
	s.m.Unlock()
}
func (s *xunitSuite) TestError(tc xunitTestcase, message, value string) {
	tc.Error = &xunitTestcaseResult{
		Message: message,
		Value:   value,
		Type:    "go.error",
	}

	s.m.Lock()
	s.Errors++
	s.addTestCase(tc)
	s.m.Unlock()
}

func (s *xunitSuite) TestSkip(tc xunitTestcase) {
	tc.Skipped = true

	s.m.Lock()
	s.Skipped++
	s.addTestCase(tc)
	s.m.Unlock()
}

func (s *xunitSuite) TestSuccess(tc xunitTestcase) {
	s.m.Lock()
	s.addTestCase(tc)
	s.m.Unlock()
}

func (s *xunitSuite) addTestCase(tc xunitTestcase) {
	s.Tests++
	s.Testcases = append(s.Testcases, tc)
	s.Time = time.Since(s.Timestamp).Seconds()
}

type xunitTestcase struct {
	Name      string  `xml:"name,attr,omitempty"`
	Classname string  `xml:"classname,attr,omitempty"`
	Time      float64 `xml:"time,attr"`

	File string `xml:"file,attr,omitempty"`
	Line int    `xml:"line,attr,omitempty"`

	Failure *xunitTestcaseResult `xml:"failure,omitempty"`
	Error   *xunitTestcaseResult `xml:"error,omitempty"`
	Skipped bool                 `xml:"skipped,omitempty"`
}

type xunitTestcaseResult struct {
	Message string `xml:"message,attr,omitempty"`
	Type    string `xml:"type,attr,omitempty"`
	Value   string `xml:",innerxml"`
}

type xunitWriter struct {
	outputWriter
	m      sync.Mutex
	writer io.Writer
	stream bool
	suites map[string]*xunitSuite

	systemOut io.Writer
}

// creates new writer for xUnit reports
// "writer" here is used for logging purpose
func newXunitWriter(writer io.Writer, stream bool) *xunitWriter {
	return &xunitWriter{
		writer: writer,
		stream: stream,
		suites: make(map[string]*xunitSuite),
	}
}

func (w *xunitWriter) GetReport() ([]byte, error) {
	report := xunitReport{}
	report.Suites = make([]xunitSuite, 0, len(w.suites))
	for k := range w.suites {
		report.Suites = append(report.Suites, *w.suites[k])
	}

	return xml.MarshalIndent(report, "", "    ")
}

func (w *xunitWriter) Write(content []byte) (n int, err error) {
	if w.writer == nil {
		return
	}
	w.m.Lock()
	n, err = w.writer.Write(content)
	w.m.Unlock()
	return
}

func (w *xunitWriter) WriteCallStarted(label string, c *C) {
	w.getSuite(c) // init suite if not yet existing
}

func (w *xunitWriter) WriteCallSkipped(label string, c *C) {
	res := w.newTestcase(c)
	if !isAutogenerated(res.File) {
		w.getSuite(c).TestSkip(res)
	}
}

func (w *xunitWriter) WriteCallFailure(label string, c *C) {
	res := w.newTestcase(c)
	if !isAutogenerated(res.File) {
		message := strings.TrimSpace(c.logb.String())
		w.getSuite(c).TestFail(res, label, message)
	}
}

func (w *xunitWriter) WriteCallError(label string, c *C) {
	res := w.newTestcase(c)
	if !isAutogenerated(res.File) {
		message := strings.TrimSpace(c.logb.String())
		w.getSuite(c).TestError(res, label, message)
	}
}

func (w *xunitWriter) WriteCallSuccess(label string, c *C) {
	res := w.newTestcase(c)
	if !isAutogenerated(res.File) {
		w.getSuite(c).TestSuccess(res)
	}
}

func (w *xunitWriter) StreamEnabled() bool { return w.stream }

// qualifiedSuiteName prefixes a suite name with the import path of the
// package defining it, e.g. "example.com/project/api.TasksTests". Bare suite
// names repeat freely across a large repository, and CI systems group test
// cases by classname, so unqualified names silently merge unrelated suites.
func qualifiedSuiteName(c *C) string {
	name := c.method.suiteName()
	if pkg := getFuncPackagePath(c.method.PC()); pkg != "" {
		return pkg + "." + name
	}
	return name
}

func (w *xunitWriter) getSuite(c *C) (suite *xunitSuite) {
	var ok bool
	key := qualifiedSuiteName(c)
	w.m.Lock()
	if suite, ok = w.suites[key]; !ok {
		suite = &xunitSuite{
			Name:      c.method.suiteName(),
			Package:   getFuncPackagePath(c.method.PC()),
			Timestamp: c.startTime,
		}
		w.suites[key] = suite
	}
	w.m.Unlock()

	return
}

func (w *xunitWriter) newTestcase(c *C) xunitTestcase {
	file, line := getFuncPosition(c.method.PC())
	return xunitTestcase{
		Name:      c.testName,
		Classname: qualifiedSuiteName(c),
		File:      file,
		Line:      line,
		Time:      time.Since(c.startTime).Seconds(),
	}
}

func isAutogenerated(filename string) bool {
	return filename == "<autogenerated>"
}
