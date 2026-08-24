package main

import (
	"bytes"
	"flag"
	"fmt"
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

// benchResult is one submitted job's outcome. `elapsed` is the time to
// *accept* the job (Print-Job round trip only - this is what a queue-based
// architecture returns to the client immediately). `completed`, when
// -wait-completion is set, is the additional time observed until the job
// actually finished processing server-side (job-state reaches a terminal
// value) - this is the fair number to compare against a synchronous path
// like SumatraPDF -print-to, which blocks until rendering+dispatch is done.
type benchResult struct {
	target    target
	elapsed   time.Duration
	completed time.Duration // 0 if -wait-completion was not set or polling failed
	ok        bool
	status    string
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
	fs.Parse(args)

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
				fmt.Printf("invalid port %q: %v\n", p, err)
				os.Exit(1)
			}
			targets = append(targets, target{port: n, path: *path})
		}
	}
	if len(targets) == 0 {
		fmt.Println("error: -ports or -paths must list at least one target")
		os.Exit(1)
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Printf("error reading file %s: %v\n", *file, err)
		os.Exit(1)
	}

	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = t.label()
	}
	fmt.Printf("bench: %d requests, concurrency=%d, %d target(s) (%v), file=%s (%d bytes)\n",
		*requests, *concurrency, len(targets), labels, *file, len(data))

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
				resp, err := sendIPP(endpoint, req, bytes.NewReader(data))
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
						r.completed = pollJobCompletion(*host, tgt.port, tgt.path, jobID, *pollInterval, *pollTimeout) + elapsed
					}
				}

				results <- r
			}
		}()
	}

	wg.Wait()
	close(results)
	total := time.Since(start)

	report(results, total, targets)
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
// reached, returning the time spent polling.
func pollJobCompletion(host string, port int, path string, jobID int32, interval, timeout time.Duration) time.Duration {
	endpoint := httpEndpoint(host, port, path)
	uri := printerURI(host, port, path)
	start := time.Now()
	deadline := start.Add(timeout)

	for time.Now().Before(deadline) {
		req := buildRequest(opGetJobAttributes, 1, uri, currentUser(), func(buf *bytes.Buffer) {
			writeIntegerAttribute(buf, tagInteger, "job-id", jobID)
		})
		resp, err := sendIPP(endpoint, req, nil)
		if err == nil && resp.StatusCode < 0x0100 {
			state := findIntAttr(resp.Attributes, "job-state")
			if state == 7 || state == 8 || state == 9 {
				return time.Since(start)
			}
		}
		time.Sleep(interval)
	}
	return timeout
}

func report(results <-chan benchResult, total time.Duration, targets []target) {
	type stat struct {
		count, fail int
		durations   []time.Duration
	}
	byTarget := map[string]*stat{}
	completedByTarget := map[string]*stat{}
	overall := &stat{}
	completedOverall := &stat{}
	sawCompletion := false

	for r := range results {
		key := r.target.label()
		s, ok := byTarget[key]
		if !ok {
			s = &stat{}
			byTarget[key] = s
		}
		s.count++
		overall.count++
		s.durations = append(s.durations, r.elapsed)
		overall.durations = append(overall.durations, r.elapsed)
		if !r.ok {
			s.fail++
			overall.fail++
			fmt.Printf("  [FAIL] target=%s status=%s elapsed=%v\n", key, r.status, r.elapsed)
		} else if r.completed > 0 {
			sawCompletion = true
			completedOverall.count++
			completedOverall.durations = append(completedOverall.durations, r.completed)
			cs, ok := completedByTarget[key]
			if !ok {
				cs = &stat{}
				completedByTarget[key] = cs
			}
			cs.count++
			cs.durations = append(cs.durations, r.completed)
		}
	}

	printStat := func(label string, s *stat) {
		if s.count == 0 {
			return
		}
		sort.Slice(s.durations, func(i, j int) bool { return s.durations[i] < s.durations[j] })
		pct := func(p float64) time.Duration {
			idx := int(float64(len(s.durations)-1) * p)
			return s.durations[idx]
		}
		var sum time.Duration
		for _, d := range s.durations {
			sum += d
		}
		avg := sum / time.Duration(len(s.durations))
		fmt.Printf("%-20s count=%-4d fail=%-3d min=%-8v avg=%-8v p50=%-8v p95=%-8v max=%-8v\n",
			label, s.count, s.fail, s.durations[0], avg, pct(0.50), pct(0.95), s.durations[len(s.durations)-1])
	}

	fmt.Println()
	fmt.Println("=== per-target ===")
	for _, t := range targets {
		if s, ok := byTarget[t.label()]; ok {
			printStat(t.label(), s)
		}
	}
	fmt.Println()
	fmt.Println("=== overall (accept latency - time until Print-Job returns) ===")
	printStat("all", overall)
	fmt.Printf("total wall time: %v, throughput: %.1f req/s\n", total, float64(overall.count)/total.Seconds())

	if sawCompletion {
		fmt.Println()
		fmt.Println("=== per-target (completion latency - time until job-state reaches a terminal value) ===")
		for _, t := range targets {
			if s, ok := completedByTarget[t.label()]; ok {
				printStat(t.label(), s)
			}
		}
		fmt.Println()
		fmt.Println("=== overall (completion latency - time until job-state reaches a terminal value) ===")
		printStat("all", completedOverall)
	}
}
