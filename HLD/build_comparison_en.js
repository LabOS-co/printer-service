const fs = require("fs");
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
  PageBreak, VerticalAlign, LevelFormat,
} = require("docx");

const FONT = "Arial";

function P(text, opts = {}) {
  const { bold, size, color, italic, alignment = AlignmentType.LEFT, spacingAfter = 120, spacingBefore } = opts;
  return new Paragraph({
    alignment,
    spacing: { after: spacingAfter, before: spacingBefore || 0 },
    children: [new TextRun({ text, bold, italics: italic, size, color, font: FONT })],
  });
}

function Bullet(text, opts = {}) {
  return new Paragraph({
    alignment: AlignmentType.LEFT,
    numbering: { reference: "bullet-list", level: 0 },
    spacing: { after: 80 },
    children: [new TextRun({ text, font: FONT, bold: opts.bold, size: opts.size })],
  });
}

function H1(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_1,
    alignment: AlignmentType.LEFT,
    spacing: { before: 360, after: 160 },
    children: [new TextRun({ text, font: FONT, bold: true, color: "1F4E78" })],
  });
}

function Callout(label, text, color = "DDEBF7", borderColor = "2E74B5") {
  return new Table({
    width: { size: 100, type: WidthType.PERCENTAGE },
    borders: {
      top: { style: BorderStyle.SINGLE, size: 8, color: borderColor },
      bottom: { style: BorderStyle.SINGLE, size: 8, color: borderColor },
      left: { style: BorderStyle.SINGLE, size: 8, color: borderColor },
      right: { style: BorderStyle.SINGLE, size: 8, color: borderColor },
      insideHorizontal: { style: BorderStyle.NONE, size: 0, color: "FFFFFF" },
      insideVertical: { style: BorderStyle.NONE, size: 0, color: "FFFFFF" },
    },
    rows: [new TableRow({
      children: [new TableCell({
        shading: { type: ShadingType.CLEAR, fill: color },
        margins: { top: 150, bottom: 150, left: 200, right: 200 },
        children: [
          new Paragraph({
            alignment: AlignmentType.LEFT, spacing: { after: 60 },
            children: [new TextRun({ text: label, bold: true, font: FONT, color: borderColor, size: 22 })],
          }),
          new Paragraph({
            alignment: AlignmentType.LEFT,
            children: [new TextRun({ text, font: FONT, size: 21 })],
          }),
        ],
      })],
    })],
  });
}

function Spacer(h = 120) {
  return new Paragraph({ spacing: { after: h }, children: [] });
}

function CompareTable(headers, rows, widths) {
  const total = 9000;
  const w = widths || headers.map(() => Math.floor(total / headers.length));
  const headerRow = new TableRow({
    tableHeader: true,
    children: headers.map((h, i) => new TableCell({
      width: { size: w[i], type: WidthType.DXA },
      shading: { type: ShadingType.CLEAR, fill: "2E74B5" },
      verticalAlign: VerticalAlign.CENTER,
      margins: { top: 100, bottom: 100, left: 120, right: 120 },
      children: [new Paragraph({
        alignment: AlignmentType.LEFT,
        children: [new TextRun({ text: h, bold: true, color: "FFFFFF", font: FONT, size: 21 })],
      })],
    })),
  });
  const bodyRows = rows.map((r) => new TableRow({
    children: r.map((cellText, i) => new TableCell({
      width: { size: w[i], type: WidthType.DXA },
      verticalAlign: VerticalAlign.CENTER,
      margins: { top: 100, bottom: 100, left: 120, right: 120 },
      children: (Array.isArray(cellText) ? cellText : [cellText]).map((t) => new Paragraph({
        alignment: AlignmentType.LEFT, spacing: { after: 40 },
        children: [new TextRun({ text: t, font: FONT, size: 20 })],
      })),
    })),
  }));
  return new Table({
    width: { size: total, type: WidthType.DXA },
    columnWidths: w,
    rows: [headerRow, ...bodyRows],
  });
}

const doc = new Document({
  numbering: {
    config: [{
      reference: "bullet-list",
      levels: [{ level: 0, format: LevelFormat.BULLET, text: "•", alignment: AlignmentType.LEFT,
        style: { paragraph: { indent: { left: 420, hanging: 260 } } } }],
    }],
  },
  styles: {
    default: {
      document: { run: { font: FONT, size: 21 } },
      heading1: { run: { font: FONT, bold: true, size: 30, color: "1F4E78" }, paragraph: { spacing: { before: 360, after: 160 } } },
    },
  },
  sections: [{
    properties: { page: { size: { width: 12240, height: 15840 } } }, // US Letter
    children: [
      // ---------- Title ----------
      new Paragraph({ spacing: { before: 1200, after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "CUPS+IPP vs. Windows Print Spooler", bold: true, size: 52, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "Performance comparison — latency, throughput, CPU and memory", size: 26, font: FONT, color: "2E74B5" })] }),
      new Paragraph({ spacing: { after: 600 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "LAB-16894 — appendix to the print server spec", size: 20, italics: true, font: FONT, color: "555555" })] }),
      Callout("Test methodology", "CUPS side: 700 print jobs total, spread across 15 CUPS queues / virtual printers (10 declaring direct PDF support, 5 not), concurrency=20. Windows side: 60 print jobs, concurrency=5, via SumatraPDF -print-to against a file-backed printer using a real Brother driver (not PORTPROMPT, to avoid hanging on a save dialog) — a larger sample than the first quick run, without wasting real paper or waiting hours (SumatraPDF is far slower, ~3.3s/job on average). Same PDF file (2 pages, A4) on both sides.", "FFF2CC", "BF8F00"),
      new Paragraph({ children: [new PageBreak()] }),

      H1("1. Main comparison table"),
      P("CUPS side: average across all 700 print jobs (15 printers). Windows side: average across 60 print jobs."),
      CompareTable(
        ["Metric", "CUPS + IPP (700 jobs, 15 printers)", "Windows Spooler (60 jobs)", "Winner"],
        [
          ["Client-facing latency (per job)", "min 44.5ms / avg 62.1ms / p95 94.8ms / max 127.5ms", "min 3.15s / avg 3.36s / p95 3.52s / max 3.59s", "CUPS — ~54x"],
          ["Throughput (requests/sec)", "321.5 req/s", "1.5 req/s", "CUPS — ~214x"],
          ["Failures", "0 out of 700", "0 out of 60", "tied"],
          ["Rendering CPU cost (PDF→raster)", "~0.63 CPU-sec/job (Ghostscript, only 5 of 15 printers)", "~3.09 CPU-sec/job (SumatraPDF, every job)", "CUPS/Ghostscript — ~5x more efficient"],
          ["Peak concurrent memory (~5 concurrent instances)", "~153MB", "~2,650MB (2.65GB)", "CUPS/Ghostscript — ~17x less"],
          ["Orchestrator cost itself (cupsd/spoolsv)", "~12.5MB, ~0.12 CPU-sec total", "~47MB, ~0 CPU-sec", "similar, negligible on both sides"],
        ],
        [2400, 2700, 2400, 1500]
      ),
      Spacer(160),

      H1("2. Full breakdown for all 15 printers (the 700 print jobs)"),
      P("Each row = ~47 print jobs (700 split across 15 printers). All 700 succeeded (0 failures), after raising CUPS's MaxJobs limit (see section 4)."),
      CompareTable(
        ["#", "Manufacturer / Model", "Direct PDF?", "Count", "min", "avg", "p95", "max"],
        [
          ["1", "HP LaserJet Pro M404", "Yes", "47", "45.0ms", "62.1ms", "90.9ms", "111.4ms"],
          ["2", "Canon imageRUNNER ADVANCE", "Yes", "47", "45.0ms", "61.8ms", "84.7ms", "108.5ms"],
          ["3", "Xerox VersaLink C405", "Yes", "47", "44.8ms", "61.8ms", "88.4ms", "108.4ms"],
          ["4", "Epson WorkForce Pro", "Yes", "47", "44.6ms", "61.9ms", "87.4ms", "106.7ms"],
          ["5", "Kyocera ECOSYS M2540", "Yes", "47", "44.5ms", "61.4ms", "85.1ms", "106.6ms"],
          ["6", "Ricoh MP C3004", "Yes", "47", "44.5ms", "62.7ms", "88.2ms", "119.1ms"],
          ["7", "Lexmark MX521", "Yes", "47", "44.8ms", "61.9ms", "90.2ms", "116.0ms"],
          ["8", "Konica Minolta bizhub C3350", "Yes", "47", "44.8ms", "62.2ms", "90.4ms", "115.9ms"],
          ["9", "Sharp MX-3070", "Yes", "47", "45.1ms", "62.4ms", "92.7ms", "115.8ms"],
          ["10", "Dell Smart Printer S2815", "Yes", "47", "44.9ms", "62.8ms", "94.8ms", "115.7ms"],
          ["11", "Brother HL-L2350DW", "No", "46", "44.9ms", "62.0ms", "89.9ms", "127.5ms"],
          ["12", "Samsung ProXpress M4020", "No", "46", "44.9ms", "62.2ms", "90.0ms", "114.8ms"],
          ["13", "Zebra ZT411", "No", "46", "44.9ms", "61.8ms", "89.8ms", "113.2ms"],
          ["14", "Star TSP143", "No", "46", "44.9ms", "62.4ms", "91.9ms", "112.6ms"],
          ["15", "OKI B432", "No", "46", "45.1ms", "61.9ms", "87.8ms", "112.5ms"],
        ],
        [500, 2600, 1100, 900, 1000, 1100, 1000, 900]
      ),
      Spacer(120),
      Callout("Key finding from the per-printer breakdown", "Client-facing latency is statistically identical between the 10 printers that support PDF directly (no rendering on the CUPS side) and the 5 that don't (real rendering via Ghostscript) — because CUPS returns a response as soon as the job is queued, before rendering even starts. The real difference between the two groups doesn't show up here, but in background load: a separate measurement of actual Ghostscript processes showed exactly 5 invocations — matching precisely the 5 printers without direct PDF support (rows 11-15). The 10 PDF-capable printers (rows 1-10) incurred zero rendering cost on the server side.", "DDEBF7", "2E74B5"),
      Spacer(),

      H1("3. Why the gap is so large — this isn't just \"faster\", it's a different architecture"),
      P("CUPS returns a response to the client as soon as the job is queued — the actual rendering (Ghostscript, when needed) happens in the background, after the client already has a response. This is exactly the asynchronous-queue approach discussed in the spec (section 3)."),
      P("SumatraPDF -print-to, on the other hand, blocks the caller until full rendering and spooler handoff are done — a synchronous approach. This isn't a \"flaw\" on the Windows side, it's simply how such a tool is built: it has to be a full GUI application with its own PDF rendering engine just to hand content to the printer driver at all."),
      Spacer(),

      H1("4. Side finding: CUPS's MaxJobs limit"),
      Callout("Side finding", "The first attempt to run the 700 print jobs hit a real error: \"Too many active jobs.\" CUPS has a built-in limit (MaxJobs, default 500) on how many jobs can be active system-wide at once. Since the virtual printers also simulate realistic mechanical print speed (jobs stay \"active\" for a long time), 700 jobs crossed that limit. The limit was raised (MaxJobs 2000) so the run would complete cleanly. This is a real production capacity-planning consideration — not a bug in the code.", "FCE4D6", "C55A11"),
      Spacer(),

      H1("5. What was left out of the comparison, and why"),
      Bullet("\"Full completion time\" (until the job actually reaches completed, not just accepted) was also measured, but left out of the table: the virtual printer (ippeveprinter) also simulates realistic mechanical print speed (8-27 seconds per job) — this completely masks the real (fraction-of-a-second) rendering difference and says nothing about software efficiency."),
      Bullet("The Windows side had no equivalent mechanical-speed simulation (it wrote to a file, not a real printer) — so comparing \"full completion time\" between the two sides would not have been fair, which is why only metrics that genuinely reflect software/architecture cost were kept."),
      Bullet("The manufacturer/model names on the virtual printers are labels only, on the same emulator software (ippeveprinter) — the test examines our server's behavior against PDF/non-PDF scenarios, not the real hardware compatibility of any specific vendor."),
      Spacer(),

      H1("6. Conclusion"),
      Callout("Practical conclusion", "For a centralized print server (LAB-16894), the CUPS+IPP path is significantly better than the Windows Spooler+SumatraPDF path across four independent dimensions, on a large sample (700 vs. 60 print jobs): client-facing latency (~54x), throughput (~214x), rendering CPU cost (~5x), and concurrent rendering memory (~17x). Whether or not a printer supports PDF directly does not affect client-facing latency (CUPS returns a response immediately either way) — it only affects background rendering load. This reinforces the direction already recommended in the spec (based on Microsoft Universal Print / PaperCut) — a CUPS-based execution layer close to each printer, with a separate control layer that takes advantage of the queue.", "E2EFDA", "548235"),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("cups-vs-spooler-comparison-en.docx", buf);
  console.log("written");
});
