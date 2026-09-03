// Command fakelp stands in for the real `lp` binary in cups package tests —
// there is no CUPS install on the Windows dev box or in CI. It is invoked
// exactly the way cups.LPSubmitter invokes the real lp: `lp -d <mode> -t
// <title>`, document on stdin, nothing else in argv or env to steer it with
// (LPSubmitter.Submit deliberately zeroes the child's environment down to
// PATH+HOME — see lp.go — so behavior selection has to ride the one channel
// production code doesn't strip: the -d value).
//
// This file lives under testdata/ so `go build ./...` in the real module
// never touches it; the test package builds it on demand via `go build
// ./testdata/fakelp`. Stdlib only, no module dependencies, so that build
// needs no network access.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "fakelp: no mode given")
		os.Exit(2)
	}

	mode := ""
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "-d" {
			mode = os.Args[i+1]
		}
	}

	switch mode {
	case "ok":
		runOK()
	case "fail":
		fmt.Fprintln(os.Stderr, "fakelp: unable to print (simulated)")
		os.Exit(1)
	case "hang":
		// Simulates a wedged CUPS queue: never exits on its own. The test
		// relies on exec.CommandContext killing this process when ctx
		// expires — that's the P0-1 property under test.
		//
		// Not `select {}`: with a single goroutine that blocks forever
		// rather than sleeps, so the Go runtime's deadlock detector treats
		// it as "all goroutines are asleep" and crashes the process
		// immediately instead of actually hanging. time.Sleep parks the
		// goroutine without tripping that detector.
		time.Sleep(24 * time.Hour)
	default:
		fmt.Fprintf(os.Stderr, "fakelp: unknown mode %q\n", mode)
		os.Exit(2)
	}
}

// runOK dumps everything a test needs to verify Submit's invariants in one
// invocation: the exact argv the child saw (proves no spool path leaked into
// it), every env var the child saw (proves Submit's PATH/HOME-only
// allowlist actually reached the child), and a hash of the bytes read from
// stdin (proves the spooled file was piped whole, not just opened).
func runOK() {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakelp: reading stdin: %v\n", err)
		os.Exit(1)
	}
	sum := sha256.Sum256(body)

	fmt.Printf("ARGV:%s\n", strings.Join(os.Args[1:], "|"))

	env := os.Environ()
	sort.Strings(env)
	for _, e := range env {
		fmt.Printf("ENV:%s\n", e)
	}

	fmt.Printf("STDIN_LEN:%d\n", len(body))
	fmt.Printf("STDIN_SHA256:%s\n", hex.EncodeToString(sum[:]))
}
