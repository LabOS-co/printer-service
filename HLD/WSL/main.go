// printersearch is a minimal IPP client used to print a PDF directly to a
// network printer's built-in IPP endpoint, without going through the
// Windows print spooler or any local rendering tool (no SumatraPDF, no GDI).
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/user"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "info":
		runInfo(os.Args[2:])
	case "print":
		runPrint(os.Args[2:])
	case "jobs":
		runJobs(os.Args[2:])
	case "cancel":
		runCancel(os.Args[2:])
	case "bench":
		runBench(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

// usage always goes to stderr: every call site reaches it only via a usage
// mistake or an unrecognized subcommand, both already exiting 1 - and stdout
// is exactly what `> out.txt` would swallow, hiding the one message meant to
// explain the failure.
func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  printersearch info   -host <ip> [-port 631] [-path /ipp/print] [-timeout 60s]")
	fmt.Fprintln(os.Stderr, "  printersearch print  -host <ip> [-port 631] [-path /ipp/print] -file <path.pdf> [-timeout 60s]")
	fmt.Fprintln(os.Stderr, "  printersearch jobs   -host <ip> [-port 631] [-path /ipp/print] [-timeout 60s]")
	fmt.Fprintln(os.Stderr, "  printersearch cancel -host <ip> [-port 631] [-path /ipp/print] -job-id <n> [-timeout 60s]")
	fmt.Fprintln(os.Stderr, "  printersearch bench  -host <ip> -ports 9001,9002,9003 -file <path.pdf> -requests 30 -concurrency 3 [-timeout 60s]")
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "printersearch"
}

func printerURI(host string, port int, path string) string {
	return fmt.Sprintf("ipp://%s:%d%s", host, port, path)
}

func httpEndpoint(host string, port int, path string) string {
	return fmt.Sprintf("http://%s:%d%s", host, port, path)
}

// printAttributes prints one line per attribute, flagging the ones the
// printer placed under the unsupported-attributes group - the protocol's own
// way of naming which requested attributes it rejected.
func printAttributes(attrs []ippAttribute) {
	for _, a := range attrs {
		marker := ""
		if a.Group == tagUnsupportedAttributes {
			marker = "  [REJECTED BY PRINTER]"
		}
		fmt.Printf("  %-30s = %s%s\n", a.Name, a.Value, marker)
	}
}

// openDocument opens path and returns it alongside its exact size, for the
// streaming sendIPP call in runPrint to declare as an explicit
// Content-Length. That declaration is a hard contract once made: net/http
// writes at most that many bytes to the wire (io.LimitReader in its own
// transfer writer) and errors only afterward on a mismatch - so a size that
// isn't knowable up front doesn't fail loud, it silently changes what
// actually gets sent. A FIFO, /dev/stdin, a process-substitution path, or a
// procfs file all report Size()==0 from Stat, which would put a
// well-framed Print-Job carrying an EMPTY document on the wire; a regular
// file being mutated concurrently would have the wire body silently
// truncated to whatever size was true at Stat time. os.ReadFile (what this
// replaced) had no such gap, since it read the actual bytes rather than
// trusting a metadata field - hence rejecting anything that isn't a
// regular file outright, rather than only degrading gracefully.
func openDocument(path string) (*os.File, int64) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening file %s: %v\n", path, err)
		os.Exit(1)
	}
	info, err := f.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error stat'ing file %s: %v\n", path, err)
		os.Exit(1)
	}
	if !info.Mode().IsRegular() {
		fmt.Fprintf(os.Stderr, "error: %s is not a regular file; its size can't be declared up front for streaming\n", path)
		os.Exit(1)
	}
	return f, info.Size()
}

func runInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	host := fs.String("host", "", "printer IP or hostname (required)")
	port := fs.Int("port", 631, "IPP port")
	path := fs.String("path", "/ipp/print", "IPP resource path")
	timeout := fs.Duration("timeout", 0, "overall request timeout (0 = the 60s default)")
	fs.Parse(args)
	setClientTimeout(*timeout)

	if *host == "" {
		fmt.Fprintln(os.Stderr, "error: -host is required")
		os.Exit(1)
	}

	uri := printerURI(*host, *port, *path)
	endpoint := httpEndpoint(*host, *port, *path)

	req := buildRequest(opGetPrinterAttributes, 1, uri, currentUser(), nil)

	fmt.Printf("Sending Get-Printer-Attributes to %s\n", endpoint)
	resp, err := sendIPP(endpoint, req, nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("IPP version: %d.%d\n", resp.Version>>8, resp.Version&0xFF)
	fmt.Printf("Status: %s (0x%04x)\n", statusName(resp.StatusCode), resp.StatusCode)
	printAttributes(resp.Attributes)
}

func runPrint(args []string) {
	fs := flag.NewFlagSet("print", flag.ExitOnError)
	host := fs.String("host", "", "printer IP or hostname (required)")
	port := fs.Int("port", 631, "IPP port")
	path := fs.String("path", "/ipp/print", "IPP resource path")
	file := fs.String("file", "", "path to the PDF file to print (required)")
	jobName := fs.String("job-name", "", "job name (defaults to the file name)")
	media := fs.String("media", "iso_a4_210x297mm", "media size keyword (explicit, avoids relying on printer auto-detect)")
	colorMode := fs.String("color-mode", "monochrome", "print-color-mode keyword")
	quality := fs.Int("quality", 4, "print-quality enum value")
	copies := fs.Int("copies", 1, "number of copies")
	firstPage := fs.Int("first-page", 0, "first page to print (page-ranges); 0 = print all pages (default)")
	lastPage := fs.Int("last-page", 0, "last page to print (page-ranges); 0 = print all pages (default)")
	resolution := fs.Int("resolution", 300, "print-resolution in dpi (explicit, avoids gs falling back to 600dpi/8-bit which overflowed this printer's spool area)")
	hold := fs.Bool("hold", false, "submit with job-hold-until=indefinite instead of printing immediately (for inspection)")
	timeout := fs.Duration("timeout", 0, "overall request timeout (0 = the 60s default)")
	fs.Parse(args)
	setClientTimeout(*timeout)

	if *host == "" || *file == "" {
		fmt.Fprintln(os.Stderr, "error: -host and -file are required")
		os.Exit(1)
	}

	// Streamed via os.Open rather than os.ReadFile (B4): the whole point of
	// bench.go's own os.ReadFile is loading the payload once so disk I/O
	// doesn't skew concurrently-measured latency (see bench.go's doc
	// comment) - runPrint is the one-shot CLI path with no such measurement
	// to protect, so there's no reason to hold the entire document in
	// memory here.
	doc, size := openDocument(*file)
	defer doc.Close()

	name := *jobName
	if name == "" {
		name = *file
	}

	uri := printerURI(*host, *port, *path)
	endpoint := httpEndpoint(*host, *port, *path)

	req := buildRequest(opPrintJob, 1, uri, currentUser(), func(buf *bytes.Buffer) {
		writeAttribute(buf, tagMimeMediaType, "document-format", "application/pdf")
		writeAttribute(buf, tagNameWithoutLang, "job-name", name)

		// Explicit job-attributes group: pin down media/color/quality/copies
		// instead of relying on the printer's auto-detected defaults, since
		// this printer's IPP capability response has a malformed
		// attributes-natural-language field that confused CUPS's driverless
		// autoconfiguration and produced a corrupt oversized raster job.
		buf.WriteByte(tagJobAttributes)
		writeAttribute(buf, tagKeyword, "media", *media)
		writeAttribute(buf, tagKeyword, "print-color-mode", *colorMode)
		writeAttribute(buf, tagKeyword, "sides", "one-sided")
		writeResolutionAttribute(buf, "printer-resolution", int32(*resolution), int32(*resolution), true)
		writeEnumAttribute(buf, "print-quality", int32(*quality))
		writeIntegerAttribute(buf, tagInteger, "copies", int32(*copies))
		if *firstPage > 0 && *lastPage > 0 {
			writeRangeOfIntegerAttribute(buf, "page-ranges", int32(*firstPage), int32(*lastPage))
		}
		if *hold {
			writeAttribute(buf, tagKeyword, "job-hold-until", "indefinite")
		}
	})

	fmt.Printf("Sending Print-Job (%d bytes) to %s\n", size, endpoint)
	resp, err := sendIPP(endpoint, req, doc, size)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Status: %s (0x%04x)\n", statusName(resp.StatusCode), resp.StatusCode)
	printAttributes(resp.Attributes)

	// Only 0x0000 is a clean success. 0x0001/0x0002 mean the printer accepted
	// the job but ignored, substituted, or found conflicting job attributes -
	// the exact way a pinned media/printer-resolution attribute can be
	// silently dropped and produce a corrupt print while looking like success.
	if resp.StatusCode != 0x0000 {
		fmt.Fprintf(os.Stderr, "FAILED: printer reported %s, not a clean success - check for [REJECTED BY PRINTER] attributes above.\n", statusName(resp.StatusCode))
		os.Exit(1)
	}
	fmt.Println("Print job submitted successfully.")
}

func runJobs(args []string) {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	host := fs.String("host", "", "printer IP or hostname (required)")
	port := fs.Int("port", 631, "IPP port")
	path := fs.String("path", "/ipp/print", "IPP resource path")
	timeout := fs.Duration("timeout", 0, "overall request timeout (0 = the 60s default)")
	fs.Parse(args)
	setClientTimeout(*timeout)

	if *host == "" {
		fmt.Fprintln(os.Stderr, "error: -host is required")
		os.Exit(1)
	}

	uri := printerURI(*host, *port, *path)
	endpoint := httpEndpoint(*host, *port, *path)

	req := buildRequest(opGetJobs, 1, uri, currentUser(), func(buf *bytes.Buffer) {
		writeAttribute(buf, tagKeyword, "which-jobs", "not-completed")
		buf.WriteByte(tagBoolean)
		binary.Write(buf, binary.BigEndian, uint16(len("my-jobs")))
		buf.WriteString("my-jobs")
		binary.Write(buf, binary.BigEndian, uint16(1))
		buf.WriteByte(0)
	})

	resp, err := sendIPP(endpoint, req, nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Status: %s (0x%04x)\n", statusName(resp.StatusCode), resp.StatusCode)
	printAttributes(resp.Attributes)
}

func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	host := fs.String("host", "", "printer IP or hostname (required)")
	port := fs.Int("port", 631, "IPP port")
	path := fs.String("path", "/ipp/print", "IPP resource path")
	jobID := fs.Int("job-id", 0, "job id to cancel (required)")
	timeout := fs.Duration("timeout", 0, "overall request timeout (0 = the 60s default)")
	fs.Parse(args)
	setClientTimeout(*timeout)

	if *host == "" || *jobID == 0 {
		fmt.Fprintln(os.Stderr, "error: -host and -job-id are required")
		os.Exit(1)
	}

	uri := printerURI(*host, *port, *path)
	endpoint := httpEndpoint(*host, *port, *path)

	req := buildRequest(opCancelJob, 1, uri, currentUser(), func(buf *bytes.Buffer) {
		writeIntegerAttribute(buf, tagInteger, "job-id", int32(*jobID))
	})

	resp, err := sendIPP(endpoint, req, nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Status: %s (0x%04x)\n", statusName(resp.StatusCode), resp.StatusCode)
	printAttributes(resp.Attributes)
}
