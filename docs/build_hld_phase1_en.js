const fs = require("fs");
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
  TableOfContents, PageBreak, VerticalAlign, LevelFormat,
} = require("docx");

const FONT = "Calibri";

function P(text, opts = {}) {
  const { bold, size, color, italic, alignment = AlignmentType.LEFT, spacingAfter = 120, heading, spacingBefore } = opts;
  return new Paragraph({
    heading, alignment,
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
    heading: HeadingLevel.HEADING_1, alignment: AlignmentType.LEFT,
    spacing: { before: 360, after: 160 },
    children: [new TextRun({ text, font: FONT, bold: true, color: "1F4E78" })],
  });
}
function H2(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_2, alignment: AlignmentType.LEFT,
    spacing: { before: 240, after: 120 },
    children: [new TextRun({ text, font: FONT, bold: true, color: "2E74B5" })],
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
          new Paragraph({ alignment: AlignmentType.LEFT, spacing: { after: 60 },
            children: [new TextRun({ text: label, bold: true, font: FONT, color: borderColor, size: 22 })] }),
          new Paragraph({ alignment: AlignmentType.LEFT,
            children: [new TextRun({ text, font: FONT, size: 21 })] }),
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
      children: [new Paragraph({ alignment: AlignmentType.LEFT,
        children: [new TextRun({ text: h, bold: true, color: "FFFFFF", font: FONT, size: 21 })] })],
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
  return new Table({ width: { size: total, type: WidthType.DXA }, columnWidths: w, rows: [headerRow, ...bodyRows] });
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
      heading2: { run: { font: FONT, bold: true, size: 25, color: "2E74B5" }, paragraph: { spacing: { before: 240, after: 120 } } },
    },
  },
  sections: [{
    properties: { page: { size: { width: 11906, height: 16838 } } },
    children: [
      new Paragraph({ spacing: { before: 1200, after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "Conclusions Document — HLD Foundation", bold: true, size: 52, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "Central Print Server — Gateway and Worker Service", size: 26, font: FONT, color: "2E74B5" })] }),
      new Paragraph({ spacing: { after: 80 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "LAB-16894", size: 24, font: FONT })] }),
      Callout(
        "Purpose of this document",
        "This document presents the proposed direction for building the print service, and serves as a basis for discussion with management and system architects. Topics with a clear current direction, based on what has already been tested in practice, are presented directly. Topics with several real alternatives that haven't been decided yet, marked as “Still open”, are presented as options with a recommendation, for joint discussion and decision. Nothing in this document is final — even what is shown here as a clear recommendation can be changed by management. This phase (Phase 1) focuses on a single-machine deployment; expansion to multiple machines is described briefly as Phase 2 at the end of the document.",
        "FFF2CC", "BF8F00"
      ),
      new Paragraph({ children: [new PageBreak()] }),

      H1("Table of Contents"),
      new TableOfContents("Table of Contents", { hyperlink: true, headingStyleRange: "1-2" }),
      new Paragraph({ children: [new PageBreak()] }),

      H1("1. General Architecture"),
      P("The general flow, from sending the request to the paper coming out:"),
      Bullet("The client (our own system that sends print requests) sends a print request to the Gateway's REST API."),
      Bullet("The Gateway handles the request. Exactly when the response returns to the client — immediately, or only after printing actually finishes — is still an open decision, see section 4.1."),
      Bullet("The service handling the request hands the file to CUPS (via ippfix), over IPP."),
      Bullet("CUPS sends the print job to the physical printer."),
      Bullet("The client receives or checks the final status — the exact mechanism is also still open, see section 5."),
      Spacer(80),
      P("Deployment layout (Phase 1): a single Linux machine, running:", { bold: true }),
      Bullet("Two identical services (Gateway/Worker) — at least 2, so that if one goes down or is taken out for maintenance, the other keeps working."),
      Bullet("One shared CUPS instance for both services."),
      P("This protects against a single process/service failure. It does NOT protect against the machine itself failing — that requires more than one machine (Phase 2)."),
      Spacer(),

      H1("2. Print Engine — CUPS + IPP + ippfix"),
      P("The current direction, based on tests already performed in practice: printing is done via CUPS on Linux, speaking IPP directly to the printers, with two supporting components:"),
      H2("ippfix"),
      P("A small component that sits between CUPS and the printer, fixing malformed IPP responses that some printers return. A printer was found in practice that returned invalid data (against the standard), and ippfix resolved it successfully."),
      H2("Static PPD file"),
      P("A PPD is the printer's “capability card” — paper sizes, resolution, color, etc."),
      P("The printer is always queried in practice, and its real data is used. A default value only kicks in when the actual response is missing a field or contains an invalid value — in that case only that specific problematic field is corrected from a known template, while all other data is kept exactly as the printer reported it. The final PPD file is saved so this discovery process doesn't need to repeat on every single print job."),
      H2("Performance test results"),
      P("In a test performed on the same file:"),
      Bullet("CUPS accepted print jobs about 54x faster than Windows Spooler with SumatraPDF."),
      Bullet("Ghostscript used about 5x less CPU."),
      Bullet("Ghostscript used about 17x less peak memory."),
      P("Based on this data, the proposed solution does not include Windows Spooler or SumatraPDF."),
      Spacer(),

      H1("3. High Availability (Phase 1: single machine)"),
      Bullet("Recommended: run at least 2 services on the same machine. A higher number isn't required at this stage, based on what's been tested so far."),
      Bullet("The two services share one CUPS instance. There's no need for a sync mechanism between multiple CUPS instances, since there's only one."),
      Bullet("Since CUPS is shared, it is a single critical component: if it crashes, both services lose print capability at the same time. Proposed: configure it as a service that restarts automatically (systemd) if it crashes."),
      Bullet("The dedicated print database (see section 4.2) needs regular backups like any production database."),
      Spacer(80),
      P("What this solves, and what it doesn't:", { bold: true }),
      CompareTable(
        ["Situation", "Does Phase 1 (single machine) solve it?"],
        [
          ["One service crashes or is taken down for an update", "Yes — the other keeps going"],
          ["CUPS itself crashes", "Partially — should restart automatically, but there's a window with no printing"],
          ["The whole machine goes down (hardware, OS)", "No — requires more than one machine (Phase 2)"],
        ], [3500, 5500]
      ),
      Spacer(),

      H1("4. Print Request Intake — Still Open"),
      H2("4.1 Direct request vs. queue"),
      CompareTable(
        ["", "Option 1: Direct (synchronous) request", "Option 2: Queue (asynchronous)"],
        [
          ["Advantages", "Simpler to implement", "Client isn't blocked; easier to handle load and retries"],
          ["Disadvantages", "Client waits an unknown amount of time; a printer fault stalls the request", "Requires handling statuses and progress checks"],
          ["When it fits", "Very low volume", "More than one concurrent request"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout("Recommendation", "An asynchronous queue. To the client it can still look like plain REST (submit + check status)."),
      Spacer(160),
      H2("4.2 If a queue is chosen — which infrastructure backs it?"),
      Bullet("Option A: use the existing Jobs infrastructure that the calling system (the one sending the print requests) already runs itself, based on an existing DB table."),
      Bullet("Option B: build an independent, dedicated mechanism, just for printing, separate from the calling system."),
      Spacer(80),
      Callout("Recommendation", "Option B — independent, so as not to add more load onto a database that already serves other purposes. Since this touches an existing system that isn't solely the print server's responsibility, this is a central point for discussion, not a final decision."),
      Spacer(),

      H1("5. How Print Status Is Checked — Still Open"),
      Bullet("Option 1 — shared medium: a DB table that both the general system and the print servers can access."),
      Bullet("Option 2 — status request by trace_id: the general system sends a specific request “what happened to job X”."),
      Bullet("Option 3 — update via the shared queue (depending on what's chosen in section 4.2): a status change is broadcast as a message on the queue itself."),
      Bullet("Option 4 — waiting for a response: relevant only if “direct request” was chosen in section 4.1 — then the response itself includes the final status."),
      P("Note: options 1 and 2 are really two sides of the same mechanism (a shared table + an API that reads from it). Option 4 belongs only to the synchronous path. All four options remain open for discussion.", { italic: true, size: 19 }),
      Spacer(),

      H1("6. Print File Delivery — Still Open"),
      Bullet("Option 1: the client sends the file directly (multipart)."),
      Bullet("Option 2: the client sends only a reference to the file, and the server downloads it from there."),
      Spacer(80),
      Callout("Recommendation", "Support both — direct attachment as the default up to about 10MB, and above that or for high-volume clients — a reference (S3-compatible storage, such as MinIO for an on-prem deployment). If URL download is supported, security protection is required (see section 11) so the server doesn't download from unapproved addresses."),
      Spacer(),

      H1("7. Print Request Structure"),
      CompareTable(
        ["Field", "Required?", "Note"],
        [
          ["Printer ID", "Yes", "A logical ID from the printer catalog (see section 13), not an IP address"],
          ["File or file reference", "One of the two", "See section 6"],
          ["Page size, resolution, color, page range, copies", "No", "Defaults to the printer's own defaults"],
          ["Priority", "No", "See section 12"],
          ["Callback URL", "No", "Automatic notification on completion"],
          ["Idempotency Key", "Yes", "Prevents a duplicate print on retry — see section 10"],
          ["Requester ID + trace_id", "Yes", "For tracing in logs and audit"],
        ], [2200, 1300, 5500]
      ),
      Spacer(),

      H1("8. Logging and Audit"),
      P("Proposed to separate two destinations:"),
      Bullet("Operational log (Kibana/ELK) — for engineers: troubleshooting, dashboards, alerts."),
      Bullet("Audit in a dedicated database — who printed what, when, and what the result was."),
      H2("What is stored in the Audit record"),
      CompareTable(
        ["Field", "Note"],
        [
          ["actor, action, target", "Who performed it, which action, on which job/printer"],
          ["timestamp and result", "UTC + an exact reason code"],
          ["trace_id", "Shared between the operational log and the audit trail"],
          ["source_service, request_ip, sequence_number", "For tracing and verifying records weren't altered"],
        ], [3000, 6000]
      ),
      Spacer(120),
      P("Proposed that Audit records be append-only (cannot be deleted/edited), linked in a chain that reveals tampering, and automatically verified at least once a day."),
      CompareTable(
        ["", "Operational log (Kibana)", "Audit (dedicated DB)"],
        [
          ["Retention", "30–90 days", "Significantly longer, per compliance requirements"],
          ["Mutable", "Yes", "No"],
        ], [1800, 3600, 3600]
      ),
      Spacer(),

      H1("9. Failure Handling and Retry Policy"),
      CompareTable(
        ["Failure type", "Example", "Handling"],
        [
          ["Transient", "Printer busy, momentary network fault", "Automatic retry, with growing backoff"],
          ["Permanent", "Printer doesn't exist, corrupt file", "Immediate failure + alert"],
          ["Ambiguous", "Connection closed after send, before ack", "Check against CUPS/the printer before retrying"],
        ], [2000, 3400, 3600]
      ),
      Spacer(120),
      Callout("Recommendation", "3 to 5 attempts. After all fail — the job moves to a “dead-letter queue” (DLQ) with full details, and someone gets alerted and reviews it."),
      Spacer(),

      H1("10. Crash Recovery and Preventing Duplicate Prints"),
      Bullet("Every request has a unique key (Idempotency Key). Before retrying, check whether a successful print already happened with the same key."),
      Bullet("A job is “locked” for a bounded time while being handled. If the handling service crashes, the lock expires and the job automatically returns to the queue."),
      Bullet("No permanent “leader” mechanism is needed between the services — a time-bounded lock plus a unique key are sufficient."),
      H2("The most sensitive case: unclear whether the print went out"),
      P("It's possible the page already came out of the printer, but the service crashed before marking the job as successful."),
      Spacer(80),
      Callout("Recommendation", "Before retrying, check against CUPS/the printer whatever can be checked. If there's still no certainty — print again. The trade-off: a small risk of an occasional duplicate print, versus the risk of a “lost” print."),
      Spacer(160),
      H2("Graceful shutdown"),
      P("On receiving a stop signal: stop accepting new jobs immediately, wait for jobs already in progress to finish (up to a reasonable time limit), and only then shut down."),
      Spacer(),

      H1("11. Security"),
      H2("11.1 Service-to-service authentication"),
      CompareTable(
        ["Option", "When it fits"],
        [
          ["mTLS", "Proposed default — a relatively stable set of callers"],
          ["API keys", "Temporary solution only"],
          ["OAuth2 Client Credentials", "If an organizational identity provider already exists"],
        ], [3000, 6000]
      ),
      Spacer(120),
      H2("11.2 Hardening Ghostscript and the service"),
      Bullet("Updated Ghostscript version (older versions have documented security bugs)."),
      Bullet("Run without root privileges, no shell access."),
      Bullet("Read-only filesystem."),
      Bullet("CPU and memory limits per print job."),
      Bullet("No network access from the file-processing process itself."),
      H2("11.3 Protection against URL misuse (if option 2 from section 6 is supported)"),
      Bullet("Download only from pre-approved sources."),
      Bullet("Check the actual IP address before and after connecting, to block attempts to reach the internal network."),
      Spacer(),

      H1("12. Priorities and Load"),
      Bullet("Each printer has a separate queue, so a stuck printer doesn't delay other printers. A job waiting for a faulty printer is not abandoned — it's still handled per the failure policy (section 9). The “separate queue” only prevents it from delaying other, working printers."),
      Bullet("At least two priority levels (urgent/normal)."),
      Bullet("No rate limit rejects or blocks any caller. Every request is accepted and enters the queue, even under high load."),
      Bullet("There is monitoring and alerting if a single caller sends an abnormal volume — for awareness only, not to block it."),
      Bullet("A site with high print volume is handled by adding servers at that site, not by limiting clients."),
      Spacer(),

      H1("13. Printer Catalog and Onboarding"),
      P("Where it's stored: in the same dedicated print database (section 4.2, option B), not in the calling system's database."),
      P("What it includes, per printer: logical ID, address, PPD file and capabilities, whether it needs ippfix, and which options are supported."),
      P("What this actually saves:", { bold: true }),
      Bullet("No need to run a live negotiation with the printer on every single print."),
      Bullet("A printer can be referenced by a logical ID instead of a hardcoded IP address in code."),
      Bullet("Feeds the status view (section 15)."),
      P("Proposed process for onboarding a new printer: capability check, checking for known IPP issues, generating a static PPD, and distributing the configuration to the services."),
      Spacer(),

      H1("14. Resource Estimate (Phase 1: single machine)"),
      CompareTable(
        ["Component", "Estimate"],
        [
          ["Two Gateway/Worker services (excluding CUPS)", "0.5–1 vCPU and 256–512MB memory, per service"],
          ["CUPS + ippfix + Ghostscript", "1–2 vCPU and 512MB–1GB, depending on concurrent print volume"],
        ], [4000, 5000]
      ),
      Spacer(),

      H1("15. Monitoring and Print Status Visibility"),
      H2("What CUPS provides by default"),
      P("CUPS has a built-in web interface (port 631) that shows the printer list, jobs and history, and allows pausing/canceling jobs."),
      P("Limitations: available by default only from the machine itself; basic permissions only; intended for technical management and diagnostics, not for exposure to stakeholders."),
      Spacer(80),
      Callout("Recommendation", "The CUPS interface stays available locally only, for targeted debugging by an engineer. It is not exposed as a view to other stakeholders."),
      Spacer(160),
      H2("How status is actually displayed"),
      P("Proposed: build a simple view/API inside the Gateway service itself, reading from the dedicated database and presenting a clear picture: how many requests in each status per printer, and online/offline state."),
      Spacer(80),
      H2("How to expose this externally to a dashboard — Still Open"),
      CompareTable(
        ["", "Kibana Dashboard", "Prometheus + Grafana"],
        [
          ["Advantage", "Requires no new infrastructure", "Particularly suited to numeric metrics and alerting"],
          ["Disadvantage", "Less precise for numeric metrics", "Requires running an additional system if not already in place"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout("Recommendation", "Kibana by default. If the organization already has Prometheus/Grafana for other services — better to use them instead."),
      Spacer(),

      H1("16. Failure Map and Proposed Mitigations"),
      CompareTable(
        ["Possible failure", "Impact", "Proposed mitigation"],
        [
          ["One service crashes", "No impact, the other continues", "Working as designed"],
          ["The shared CUPS crashes", "Both services temporarily lose print capability", "Automatic restart + alert"],
          ["A service crashes after the page came out, before being marked complete", "A duplicate print is possible", "Check against CUPS/the printer; if uncertain — print again"],
          ["The dedicated database goes down", "The system halts", "Regular backups, monitoring, and an immediate alert"],
          ["A single caller sends an abnormal volume", "No blocking", "Monitoring and alerting only"],
          ["A malicious file exploits a Ghostscript vulnerability", "Code execution on the server", "Hardening + ongoing version updates"],
          ["A URL reference is abused to reach the internal network", "Exposure of internal information", "Allowlist and IP validation"],
          ["A job stays stuck “in progress” for a long time", "May be lost", "Monitor maximum dwell time + a dedicated alert"],
        ], [2600, 3200, 3200]
      ),
      Spacer(),

      H1("Phase 2 (Future) — Expanding to Multiple Machines"),
      P("When a given site needs more than one machine due to high volume, a mechanism will be required to connect the machines and share job state between them. Not planned at this stage — to be addressed when the need actually arises."),
      Spacer(),

      H1("Simple Summary"),
      P("Proposed direction: a print service that runs, at this stage, on a single Linux machine — two identical services for redundancy, sharing one CUPS instance. The system handles failures, keeps an audit trail, and monitors load without blocking any client."),
      P("Topics still open for discussion:", { bold: true }),
      Bullet("Direct request vs. queue (4.1)."),
      Bullet("If queue — existing infrastructure vs. an independent mechanism (4.2)."),
      Bullet("How print status is checked — 4 alternatives (5)."),
      Bullet("File delivery (6)."),
      Bullet("External monitoring: Kibana vs. Prometheus/Grafana (15)."),
      Spacer(),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("print-gateway-hld-phase1-en.docx", buf);
  console.log("written");
});
