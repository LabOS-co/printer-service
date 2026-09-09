// Command fakelp stands in for the real `lp` binary in cups package tests,
// since there is no CUPS install on the Windows dev box or in CI. Mode is
// selected via the -d value (the only channel LPSubmitter doesn't strip from
// the child's env/argv).
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
		// Simulates a wedged CUPS queue. time.Sleep, not select{}: an
		// empty select trips Go's deadlock detector and exits immediately.
		time.Sleep(24 * time.Hour)
	default:
		fmt.Fprintf(os.Stderr, "fakelp: unknown mode %q\n", mode)
		os.Exit(2)
	}
}

// runOK reports the argv, env, and a hash of stdin it received, so a test
// can verify what Submit actually passed to the child.
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
