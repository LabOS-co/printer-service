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

func usage() {
	fmt.Println("usage:")
	fmt.Println("  printersearch info  -host <ip> [-port 631] [-path /ipp/print]")
	fmt.Println("  printersearch print -host <ip> [-port 631] [-path /ipp/print] -file <path.pdf>")
	fmt.Println("  printersearch bench -host <ip> -ports 9001,9002,9003 -file <path.pdf> -requests 30 -concurrency 3")
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

func runInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	host := fs.String("host", "", "printer IP or hostname (required)")
	port := fs.Int("port", 631, "IPP port")
	path := fs.String("path", "/ipp/print", "IPP resource path")
	fs.Parse(args)

	if *host == "" {
		fmt.Println("error: -host is required")
		os.Exit(1)
	}

	uri := printerURI(*host, *port, *path)
	endpoint := httpEndpoint(*host, *port, *path)

	req := buildRequest(opGetPrinterAttributes, 1, uri, currentUser(), nil)

	fmt.Printf("Sending Get-Printer-Attributes to %s\n", endpoint)
	resp, err := sendIPP(endpoint, req, nil)
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
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
	fs.Parse(args)

	if *host == "" || *file == "" {
		fmt.Println("error: -host and -file are required")
		os.Exit(1)
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Printf("error reading file %s: %v\n", *file, err)
		os.Exit(1)
	}

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

	fmt.Printf("Sending Print-Job (%d bytes) to %s\n", len(data), endpoint)
	resp, err := sendIPP(endpoint, req, bytes.NewReader(data))
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Status: %s (0x%04x)\n", statusName(resp.StatusCode), resp.StatusCode)
	printAttributes(resp.Attributes)

	// Only 0x0000 is a clean success. 0x0001/0x0002 mean the printer accepted
	// the job but ignored, substituted, or found conflicting job attributes -
	// the exact way a pinned media/printer-resolution attribute can be
	// silently dropped and produce a corrupt print while looking like success.
	if resp.StatusCode != 0x0000 {
		fmt.Printf("FAILED: printer reported %s, not a clean success - check for [REJECTED BY PRINTER] attributes above.\n", statusName(resp.StatusCode))
		os.Exit(1)
	}
	fmt.Println("Print job submitted successfully.")
}

func runJobs(args []string) {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	host := fs.String("host", "", "printer IP or hostname (required)")
	port := fs.Int("port", 631, "IPP port")
	path := fs.String("path", "/ipp/print", "IPP resource path")
	fs.Parse(args)

	if *host == "" {
		fmt.Println("error: -host is required")
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

	resp, err := sendIPP(endpoint, req, nil)
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
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
	fs.Parse(args)

	if *host == "" || *jobID == 0 {
		fmt.Println("error: -host and -job-id are required")
		os.Exit(1)
	}

	uri := printerURI(*host, *port, *path)
	endpoint := httpEndpoint(*host, *port, *path)

	req := buildRequest(opCancelJob, 1, uri, currentUser(), func(buf *bytes.Buffer) {
		writeIntegerAttribute(buf, tagInteger, "job-id", int32(*jobID))
	})

	resp, err := sendIPP(endpoint, req, nil)
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Status: %s (0x%04x)\n", statusName(resp.StatusCode), resp.StatusCode)
	printAttributes(resp.Attributes)
}
