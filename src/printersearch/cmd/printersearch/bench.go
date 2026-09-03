package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// target is one thing bench submits jobs to: either several virtual
// printers on different ports (same IPP path), or several CUPS queues on
// the same port but different paths (e.g. /printers/vp1, /printers/vp2).
type target struct {
	port int
	path string
}

func (t target) label() string { return fmt.Sprintf("%d%s", t.port, t.path) }

// completionOutcome enumerates what -wait-completion observed for one
// successfully-accepted job. This used to be two independent bools
// (completedOK/gaveUp), which left a third real outcome - the job was
// accepted but its job-id couldn't be parsed from the response, so completion
// could not even be attempted - with no bucket of its own: it silently
// vanished from both the "measured" and "gave up" counts (P0-8 follow-up). An
// enum makes the outcome set closed: every accepted job under
// -wait-completion lands in exactly one of these, and report() can switch
// on it exhaustively instead of relying on boolean combinations that don't
// enumerate their own states.
type completionOutcome int

const (
	completionNotAttempted completionOutcome = iota // -wait-completion was off, or the request itself failed
	completionMeasured                              // job-state reached a terminal value before -poll-timeout
	completionGaveUp                                // -poll-timeout was hit before a terminal job-state was observed
	completionNoJobID                               // job accepted, but job-id could not be parsed from the response
)

// benchResult is one submitted job's outcome. `elapsed` is the time to
// *accept* the job (Print-Job round trip only - this is what a queue-based
// architecture returns to the client immediately). `completed`, when
// `completion == completionMeasured`, is the additional time observed until
// the job actually finished processing server-side (job-state reaches a
// terminal value) - this is the fair number to compare against a synchronous
// path like SumatraPDF -print-to, which blocks until rendering+dispatch is
// done. Any other `completion` value means `completed` is not a latency
// measurement and must never be folded into the completion latency stats
// (P0-8): a give-up is "we don't know", not "it took this long."
type benchResult struct {
	target     target
	elapsed    time.Duration
	completed  time.Duration // meaningful only when completion == completionMeasured
	completion completionOutcome
	ok         bool
	status     string
}

func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "printer/emulator host")
	portsFlag := fs.String("ports", "9001,9002,9003", "comma-separated list of ports, one per virtual printer (ignored if -paths is set)")
	pathsFlag := fs.String("paths", "", "comma-separated list of IPP resource paths, one per CUPS queue, all on the same -port (e.g. /printers/vp1,/printers/vp2,/printers/vp3) - use this instead of -ports to bench queues behind a single shared CUPS instead of raw printers")
	port := fs.Int("port", 631, "port to use for all targets when -paths is set")
	path := fs.String("path", "/ipp/print", "IPP resource path to use for all targets when -ports is set")
	file := fs.String("file", "printDemo.pdf", "PDF file to submit for each job")
	requests := fs.Int("requests", 30, "total number of print jobs to submit across all workers/printers")
	concurrency := fs.Int("concurrency", 3, "number of concurrent worker goroutines")
	docFormat := fs.String("document-format", "application/octet-stream", "document-format to declare (must be in the target's document-format-supported; ippeveprinter's default only advertises application/octet-stream, image/pwg-raster, image/urf - not application/pdf)")
	media := fs.String("media", "", "media size keyword (omit to not send this job attribute at all)")
	colorMode := fs.String("color-mode", "", "print-color-mode keyword (omit to not send this job attribute at all)")
	resolution := fs.Int("resolution", 0, "printer-resolution in dpi (0 = omit this job attribute)")
	waitCompletion := fs.Bool("wait-completion", false, "after Print-Job is accepted, poll Get-Job-Attributes until job-state reaches a terminal value, and report that as a separate 'completed' latency (fair comparison against a synchronous print path)")
	pollInterval := fs.Duration("poll-interval", 20*time.Millisecond, "how often to poll job-state when -wait-completion is set")
	pollTimeout := fs.Duration("poll-timeout", 30*time.Second, "give up waiting for job completion after this long")
	jsonOutput := fs.Bool("json", false, "emit the report as JSON instead of human-readable text (for diffing numbers across runs)")
	timeout := fs.Duration("timeout", 0, "per-request timeout for every worker's HTTP client (0 = the 60s default)")
	fs.Parse(args)
	setClientTimeout(*timeout)

	if *requests <= 0 {
		fmt.Fprintln(os.Stderr, "error: -requests must be a positive number")
		os.Exit(1)
	}
	if *concurrency <= 0 {
		fmt.Fprintln(os.Stderr, "error: -concurrency must be a positive number")
		os.Exit(1)
	}

	var targets []target
	if *pathsFlag != "" {
		for _, p := range strings.Split(*pathsFlag, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			targets = append(targets, target{port: *port, path: p})
		}
	} else {
		for _, p := range strings.Split(*portsFlag, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid port %q: %v\n", p, err)
				os.Exit(1)
			}
			targets = append(targets, target{port: n, path: *path})
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "error: -ports or -paths must list at least one target")
		os.Exit(1)
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading file %s: %v\n", *file, err)
		os.Exit(1)
	}

	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = t.label()
	}
	if !*jsonOutput {
		fmt.Printf("bench: %d requests, concurrency=%d, %d target(s) (%v), file=%s (%d bytes)\n",
			*requests, *concurrency, len(targets), labels, *file, len(data))
	}

	jobs := make(chan int, *requests)
	for i := 0; i < *requests; i++ {
		jobs <- i
	}
	close(jobs)

	results := make(chan benchResult, *requests)
	var wg sync.WaitGroup

	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				tgt := targets[i%len(targets)]
				endpoint := httpEndpoint(*host, tgt.port, tgt.path)
				uri := printerURI(*host, tgt.port, tgt.path)

				req := buildRequest(opPrintJob, uint32(i+1), uri, currentUser(), func(buf *bytes.Buffer) {
					writeAttribute(buf, tagMimeMediaType, "document-format", *docFormat)
					writeAttribute(buf, tagNameWithoutLang, "job-name", fmt.Sprintf("bench-job-%d", i))
					buf.WriteByte(tagJobAttributes)
					if *media != "" {
						writeAttribute(buf, tagKeyword, "media", *media)
					}
					if *colorMode != "" {
						writeAttribute(buf, tagKeyword, "print-color-mode", *colorMode)
					}
					if *resolution > 0 {
						writeResolutionAttribute(buf, "printer-resolution", int32(*resolution), int32(*resolution), true)
					}
				})

				t0 := time.Now()
				resp, err := sendIPP(endpoint, req, bytes.NewReader(data), int64(len(data)))
				elapsed := time.Since(t0)

				r := benchResult{target: tgt, elapsed: elapsed}
				if err != nil {
					r.status = err.Error()
				} else {
					r.ok = resp.StatusCode < 0x0100
					r.status = statusName(resp.StatusCode)
				}

				if r.ok && *waitCompletion {
					jobID := findIntAttr(resp.Attributes, "job-id")
					if jobID > 0 {
						waited, ok := pollJobCompletion(*host, tgt.port, tgt.path, jobID, *pollInterval, *pollTimeout)
						r.completed = waited + elapsed
						if ok {
							r.completion = completionMeasured
						} else {
							r.completion = completionGaveUp
						}
					} else {
						r.completion = completionNoJobID
					}
				}

				results <- r
			}
		}()
	}

	wg.Wait()
	close(results)
	total := time.Since(start)

	report(results, total, targets, *jsonOutput, *waitCompletion)
}

func findIntAttr(attrs []ippAttribute, name string) int32 {
	for _, a := range attrs {
		if a.Name == name {
			if n, err := strconv.Atoi(a.Value); err == nil {
				return int32(n)
			}
		}
	}
	return 0
}

// pollJobCompletion polls Get-Job-Attributes until job-state reaches a
// terminal value (7=canceled, 8=aborted, 9=completed) or the timeout is
// reached. The bool return distinguishes a real measurement from a give-up
// (P0-8): the old single-Duration return made `return timeout` on the
// give-up path indistinguishable from "the job actually took this long",
// so a completely unresponsive target could report a fabricated
// p50=p95=poll-timeout with fail=0 instead of the failure it is.
func pollJobCompletion(host string, port int, path string, jobID int32, interval, timeout time.Duration) (time.Duration, bool) {
	endpoint := httpEndpoint(host, port, path)
	uri := printerURI(host, port, path)
	start := time.Now()
	deadline := start.Add(timeout)

	for time.Now().Before(deadline) {
		req := buildRequest(opGetJobAttributes, 1, uri, currentUser(), func(buf *bytes.Buffer) {
			writeIntegerAttribute(buf, tagInteger, "job-id", jobID)
		})
		resp, err := sendIPP(endpoint, req, nil, 0)
		if err == nil && resp.StatusCode < 0x0100 {
			state := findIntAttr(resp.Attributes, "job-state")
			if state == 7 || state == 8 || state == 9 {
				return time.Since(start), true
			}
		}
		time.Sleep(interval)
	}
	return timeout, false
}

// targetStat accumulates one target's (or the overall) results.
// successDurations/completedDurations hold ONLY successful/completed samples
// (P0-7, P0-8): a failed request's accept-latency, and a give-up's wait time,
// are counted (fail/gaveUp/noJobID) but never blended into the percentile
// arrays, since a fast connection-refused failure or a poll-timeout give-up
// is not a latency measurement and mixing it in silently drags (or flatters)
// the reported tail depending on which way the contaminating values skew.
type targetStat struct {
	successDurations   []time.Duration
	completedDurations []time.Duration
	fail               int
	gaveUp             int
	noJobID            int
}

// statSummary is the percentile computation shared by both the human-readable
// and -json report paths, so they can never silently diverge on the numbers
// themselves (formatting/labeling still lives separately in each renderer).
type statSummary struct {
	hasSamples              bool
	min, avg, p50, p95, max time.Duration
}

func summarize(durations []time.Duration) statSummary {
	if len(durations) == 0 {
		return statSummary{}
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest-rank (ceiling), not a floor-truncated index: with n<21 samples
	// the old `int((n-1)*p)` could never select the slowest sample for p95,
	// biasing the reported tail low - the direction that flatters the result.
	pct := func(p float64) time.Duration {
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		return sorted[idx]
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	return statSummary{
		hasSamples: true,
		min:        sorted[0],
		max:        sorted[len(sorted)-1],
		avg:        sum / time.Duration(len(sorted)),
		p50:        pct(0.50),
		p95:        pct(0.95),
	}
}

// throughputNote explains what the printed req/s figure actually measures.
// Under -wait-completion each worker submits a job and then blocks polling it
// to completion before picking up the next one, so wall time is dominated by
// completion polling, not submission - reporting that as a clean "Print-Job
// requests only" rate would repeat the exact mistake (a number whose label
// doesn't match what it measures) this workstream exists to eliminate.
func throughputNote(waitCompletion bool) string {
	if waitCompletion {
		return "end-to-end rate: wall time INCLUDES completion polling (-wait-completion is set) - NOT the Print-Job submission rate"
	}
	return "Print-Job submission rate"
}

func report(results <-chan benchResult, total time.Duration, targets []target, jsonOutput, waitCompletion bool) {
	byTarget := map[string]*targetStat{}
	overall := &targetStat{}

	for r := range results {
		key := r.target.label()
		s, ok := byTarget[key]
		if !ok {
			s = &targetStat{}
			byTarget[key] = s
		}
		if !r.ok {
			s.fail++
			overall.fail++
			if !jsonOutput {
				fmt.Printf("  [FAIL] target=%s status=%s elapsed=%v\n", key, r.status, r.elapsed)
			}
			continue
		}
		s.successDurations = append(s.successDurations, r.elapsed)
		overall.successDurations = append(overall.successDurations, r.elapsed)
		switch r.completion {
		case completionMeasured:
			s.completedDurations = append(s.completedDurations, r.completed)
			overall.completedDurations = append(overall.completedDurations, r.completed)
		case completionGaveUp:
			s.gaveUp++
			overall.gaveUp++
		case completionNoJobID:
			s.noJobID++
			overall.noJobID++
		case completionNotAttempted:
			// -wait-completion was off for this run; nothing to record.
		}
	}

	// Throughput counts accepted Print-Job requests only (success + fail);
	// -wait-completion polling traffic is not counted in the numerator - see
	// throughputNote for why the denominator still isn't a clean number.
	throughput := float64(len(overall.successDurations)+overall.fail) / total.Seconds()
	note := throughputNote(waitCompletion)

	if jsonOutput {
		printJSONReport(total, throughput, note, targets, byTarget, overall, waitCompletion)
		return
	}
	printTextReport(total, throughput, note, targets, byTarget, overall, waitCompletion)
}

func printTextReport(total time.Duration, throughput float64, throughputNote string, targets []target, byTarget map[string]*targetStat, overall *targetStat, waitCompletion bool) {
	// count is always the number of samples backing the printed percentiles;
	// fail/gaveup/nojobid are reported alongside as separate, explicit
	// columns rather than blended into count or into the percentiles.
	printAcceptStat := func(label string, fail int, durations []time.Duration) {
		count := len(durations)
		if count == 0 && fail == 0 {
			return
		}
		s := summarize(durations)
		if !s.hasSamples {
			fmt.Printf("%-20s count=%-4d fail=%-3d (no samples)\n", label, count, fail)
			return
		}
		fmt.Printf("%-20s count=%-4d fail=%-3d min=%-8v avg=%-8v p50=%-8v p95=%-8v max=%-8v\n",
			label, count, fail, s.min, s.avg, s.p50, s.p95, s.max)
	}
	// Unlike printAcceptStat, this never skips a target present in byTarget,
	// even when count/gaveup/nojobid are all zero: under -wait-completion a
	// target that never produced a single measured/gaveup/nojobid outcome
	// (e.g. every job to it failed outright) still gets an explicit
	// all-zero row instead of silently vanishing from the section (P0-8
	// follow-up finding).
	printCompletionStat := func(label string, gaveup, nojobid int, durations []time.Duration) {
		count := len(durations)
		s := summarize(durations)
		if !s.hasSamples {
			fmt.Printf("%-20s count=%-4d gaveup=%-3d nojobid=%-3d (no samples)\n", label, count, gaveup, nojobid)
			return
		}
		fmt.Printf("%-20s count=%-4d gaveup=%-3d nojobid=%-3d min=%-8v avg=%-8v p50=%-8v p95=%-8v max=%-8v\n",
			label, count, gaveup, nojobid, s.min, s.avg, s.p50, s.p95, s.max)
	}

	fmt.Println()
	fmt.Println("=== per-target (accept latency - time until Print-Job returns; successes only) ===")
	for _, t := range targets {
		if s, ok := byTarget[t.label()]; ok {
			printAcceptStat(t.label(), s.fail, s.successDurations)
		}
	}
	fmt.Println()
	fmt.Println("=== overall (accept latency - time until Print-Job returns; successes only) ===")
	printAcceptStat("all", overall.fail, overall.successDurations)
	fmt.Printf("total wall time: %v, throughput: %.1f req/s (%s)\n", total, throughput, throughputNote)

	if waitCompletion {
		fmt.Println()
		fmt.Println("=== per-target (completion latency - time until job-state reaches a terminal value; give-ups/unknown-job-id excluded) ===")
		for _, t := range targets {
			if s, ok := byTarget[t.label()]; ok {
				printCompletionStat(t.label(), s.gaveUp, s.noJobID, s.completedDurations)
			}
		}
		fmt.Println()
		fmt.Println("=== overall (completion latency - time until job-state reaches a terminal value; give-ups/unknown-job-id excluded) ===")
		printCompletionStat("all", overall.gaveUp, overall.noJobID, overall.completedDurations)
	}
}

// latencyStats is the JSON shape of a statSummary; embedded (not nested) into
// jsonAcceptStat/jsonCompletionStat so its fields marshal at the top level of
// each. *_ms fields are nil (omitted) when there were no samples to
// summarize - distinct from a genuine 0ms measurement.
type latencyStats struct {
	MinMS *float64 `json:"min_ms,omitempty"`
	AvgMS *float64 `json:"avg_ms,omitempty"`
	P50MS *float64 `json:"p50_ms,omitempty"`
	P95MS *float64 `json:"p95_ms,omitempty"`
	MaxMS *float64 `json:"max_ms,omitempty"`
}

func newLatencyStats(s statSummary) latencyStats {
	if !s.hasSamples {
		return latencyStats{}
	}
	ms := func(d time.Duration) *float64 { v := d.Seconds() * 1000; return &v }
	return latencyStats{MinMS: ms(s.min), AvgMS: ms(s.avg), P50MS: ms(s.p50), P95MS: ms(s.p95), MaxMS: ms(s.max)}
}

// jsonAcceptStat/jsonCompletionStat are deliberately separate types, each
// with its own constructor below, rather than one struct discriminated by a
// string/enum field - that removes the possibility (present in an earlier
// version of this fix) of a typo'd discriminator silently producing an empty
// fail/gaveup/nojobid count with no error. Fail/GaveUp/NoJobID have no
// `omitempty`: for a format whose stated purpose is diffing numbers across
// runs, a real 0 must render as `0`, not as an absent key indistinguishable
// from "this field doesn't apply here".
type jsonAcceptStat struct {
	Count int `json:"count"`
	Fail  int `json:"fail"`
	latencyStats
}

type jsonCompletionStat struct {
	Count   int `json:"count"`
	GaveUp  int `json:"gaveup"`
	NoJobID int `json:"nojobid"`
	latencyStats
}

func newAcceptStat(fail int, durations []time.Duration) jsonAcceptStat {
	return jsonAcceptStat{Count: len(durations), Fail: fail, latencyStats: newLatencyStats(summarize(durations))}
}

func newCompletionStat(gaveup, nojobid int, durations []time.Duration) jsonCompletionStat {
	return jsonCompletionStat{Count: len(durations), GaveUp: gaveup, NoJobID: nojobid, latencyStats: newLatencyStats(summarize(durations))}
}

type jsonReport struct {
	TotalWallMS       float64                       `json:"total_wall_ms"`
	ThroughputReqSec  float64                       `json:"throughput_req_per_s"`
	ThroughputNote    string                        `json:"throughput_note"`
	AcceptLatency     map[string]jsonAcceptStat     `json:"accept_latency"`
	CompletionLatency map[string]jsonCompletionStat `json:"completion_latency,omitempty"`
}

func printJSONReport(total time.Duration, throughput float64, throughputNote string, targets []target, byTarget map[string]*targetStat, overall *targetStat, waitCompletion bool) {
	rep := jsonReport{
		TotalWallMS:      total.Seconds() * 1000,
		ThroughputReqSec: throughput,
		ThroughputNote:   throughputNote,
		AcceptLatency:    map[string]jsonAcceptStat{},
	}
	for _, t := range targets {
		if s, ok := byTarget[t.label()]; ok {
			rep.AcceptLatency[t.label()] = newAcceptStat(s.fail, s.successDurations)
		}
	}
	rep.AcceptLatency["all"] = newAcceptStat(overall.fail, overall.successDurations)

	if waitCompletion {
		rep.CompletionLatency = map[string]jsonCompletionStat{}
		for _, t := range targets {
			if s, ok := byTarget[t.label()]; ok {
				rep.CompletionLatency[t.label()] = newCompletionStat(s.gaveUp, s.noJobID, s.completedDurations)
			}
		}
		rep.CompletionLatency["all"] = newCompletionStat(overall.gaveUp, overall.noJobID, overall.completedDurations)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding JSON report: %v\n", err)
		os.Exit(1)
	}
}
