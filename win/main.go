// win is a control-test tool: it prints a PDF through the normal Windows
// printing pipeline (spooler + a Windows-registered printer), instead of
// the direct CUPS/IPP path in the parent printersearch tool. Used to check
// whether the printer itself/network is fine when going through the
// standard OS print path.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	file := flag.String("file", "", "path to the PDF file to print (required)")
	printer := flag.String("printer", "", "Windows printer name as shown by Get-Printer (required)")
	sumatra := flag.String("sumatra", `\\gaia\netlims$\Autolims\MainRls\Bin\SumatraPDF.exe`, "path to SumatraPDF.exe used to drive the Windows print pipeline")
	flag.Parse()

	if *file == "" || *printer == "" {
		fmt.Println("usage: win.exe -file <path.pdf> -printer \"<Windows printer name>\"")
		os.Exit(1)
	}

	absFile, err := filepath.Abs(*file)
	if err != nil {
		fmt.Printf("error resolving file path: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Printing %s to Windows printer %q via the spooler (SumatraPDF -print-to)...\n", absFile, *printer)

	cmd := exec.Command(*sumatra, "-print-to", *printer, "-silent", absFile)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		fmt.Println(string(out))
	}
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Print command dispatched to the Windows spooler.")
}
