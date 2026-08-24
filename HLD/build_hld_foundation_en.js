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
function CodeBox(lines) {
  return new Table({
    width: { size: 100, type: WidthType.PERCENTAGE },
    borders: {
      top: { style: BorderStyle.SINGLE, size: 4, color: "AAAAAA" },
      bottom: { style: BorderStyle.SINGLE, size: 4, color: "AAAAAA" },
      left: { style: BorderStyle.SINGLE, size: 4, color: "AAAAAA" },
      right: { style: BorderStyle.SINGLE, size: 4, color: "AAAAAA" },
      insideHorizontal: { style: BorderStyle.NONE, size: 0, color: "FFFFFF" },
      insideVertical: { style: BorderStyle.NONE, size: 0, color: "FFFFFF" },
    },
    rows: [new TableRow({
      children: [new TableCell({
        shading: { type: ShadingType.CLEAR, fill: "F2F2F2" },
        margins: { top: 150, bottom: 150, left: 200, right: 200 },
        children: lines.map((l) => new Paragraph({
          alignment: AlignmentType.LEFT, spacing: { after: 20 },
          children: [new TextRun({ text: l, font: "Consolas", size: 18 })],
        })),
      })],
    })],
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
      heading2: { run: { font: FONT, bold: true, size: 25, color: "2E74B5" }, paragraph: { spacing: { before: 240, after: 120 } } },
    },
  },
  sections: [{
    properties: { page: { size: { width: 11906, height: 16838 } } },
    children: [
      // ---------- Title page ----------
      new Paragraph({ spacing: { before: 1200, after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "Conclusions Document — HLD Foundation", bold: true, size: 52, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "Central Print Server — Print Gateway / Worker Service", size: 26, font: FONT, color: "2E74B5" })] }),
      new Paragraph({ spacing: { after: 80 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "LAB-16894", size: 24, font: FONT })] }),
      Callout(
        "Purpose of this document",
        "This document states, finally and clearly, what we want to build — it is the direct input for writing the HLD. Every topic that already has a decision is written as a conclusion, with no options. In the few places where the decision genuinely hasn't been closed yet (marked “Still open”), the options are presented with a recommendation underneath. The technical foundation (CUPS + IPP + ippfix) has already been proven in practice and has physically printed on it — that is no longer an open decision point.",
        "FFF2CC", "BF8F00"
      ),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- TOC ----------
      H1("Table of Contents"),
      new TableOfContents("Table of Contents", { hyperlink: true, headingStyleRange: "1-2" }),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- 1. Architecture ----------
      H1("1. General Architecture"),
      P("The full chain of operations, from client to paper:"),
      CodeBox([
        "1. Client (Windows server / other container)  ->  Gateway REST API",
        "2. Gateway  ->  stores the request in the shared queue + database, returns an immediate ack",
        "3. Worker (on one of the nodes)  ->  pulls a pending request from the queue",
        "4. Worker  ->  hands the file to the local CUPS instance (via ippfix) over IPP",
        "5. Local CUPS  ->  actually prints to the physical printer",
        "6. Gateway  ->  updates final status (client polling / callback)",
      ]),
      Spacer(80),
      P("Three identical instances (nodes) of Gateway+Worker+CUPS+ippfix run in parallel, stateless — all real state (which requests exist, what their status is) lives in the shared queue and the database, not in the memory of any single node."),
      Spacer(),

      // ---------- 2. Print engine ----------
      H1("2. Print Engine — CUPS + IPP + ippfix"),
      P("This is a closed decision, not open for re-discussion: the print engine is CUPS on Linux, speaking IPP directly to the printers, with two supporting components of our own:"),
      Bullet("ippfix — a small reverse proxy that fixes malformed IPP fields some printers return (an RFC 8011 bug already diagnosed and fixed in practice), so CUPS can talk to them at all."),
      Bullet("Static PPD — the printer's “capability card” is generated once in advance (via ippfix), instead of being rebuilt on every print — avoiding dependence on live negotiation with a printer that may fail."),
      P("Real measured performance data (700 vs. 60 requests, same file): job-acceptance is about 54x faster than Windows Spooler+SumatraPDF (because CUPS returns an ack the moment the job is queued and renders in the background), and Ghostscript is about 5x cheaper on CPU and about 17x cheaper on peak memory than SumatraPDF per render. The Windows Spooler/SumatraPDF path is not used in this solution."),
      Spacer(),

      // ---------- 3. HA ----------
      H1("3. High Availability and Topology"),
      P("3 identical instances (minimum 2 for redundancy, but 3 is recommended — aligns with the quorum of the queue layer, see section 5). Every instance is stateless."),
      Bullet("The database (job queue + Audit) must itself run in HA (replication/cluster, e.g. Postgres) — without this, N+1 redundancy at the Gateway layer doesn't actually deliver availability, because the database itself becomes the single point of failure."),
      Bullet("Clock synchronization (NTP) across all nodes — required so that the order of events in logs and audit records is trustworthy."),
      P("There are two separate HA layers, and each solves a different kind of failure:"),
      CompareTable(
        ["", "CUPS layer (per node)", "Job queue layer"],
        [
          ["What it solves", "A printer unreachable from a specific node's point of view", "An entire node crashed or hung before finishing a request"],
          ["How it's implemented", "Each node runs a local CUPS instance + an identically named queue for the same printer; cups-browsed (CUPS's built-in implicitclass mechanism) picks whichever copy is free and routes to it", "A request is “locked” for a bounded time; if not completed, it automatically returns to the queue for another worker"],
          ["What it doesn't solve", "A container that crashes entirely — cups-browsed running on it crashes too", "Does not by itself prevent a physical duplicate print (see section 10)"],
        ],
        [1800, 3600, 3600]
      ),
      Spacer(160),
      Callout("Conclusion", "Both layers work together. CUPS itself has no built-in clustering between nodes — this is exactly the pattern that both Microsoft Universal Print and PaperCut implement: one central state, plus local print points."),
      Spacer(),

      // ---------- 4. Intake channel — OPEN ----------
      H1("4. Request Intake Channel — Still Open"),
      H2("4.1 Synchronous request vs. asynchronous queue"),
      CompareTable(
        ["", "Option 1: Direct (synchronous) request", "Option 2: Queue (asynchronous)"],
        [
          ["Advantages", "Simple to implement", "Client isn't blocked; absorbs load; automatic retry"],
          ["Disadvantages", "Client waits an unknown amount of time; a printer fault stalls the request", "Added complexity (status + polling)"],
          ["When it fits", "Very low volume, a single client", "More than one client or more than one printer — our situation"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout("Recommendation", "Option 2 — queue. To the client it can still look like plain REST (POST to submit + GET for status); the queue is internal only."),
      Spacer(160),
      H2("4.2 REST-only vs. exposing the queue directly"),
      P("Option 1: the REST API is the only external contract, the queue stays fully internal. Option 2: also expose a direct queue channel to advanced internal callers."),
      Callout("Recommendation", "Option 1 by default — a Windows caller doesn't need any queue client library at all, just plain HTTP. This also matches industry convention: almost no large company exposes a raw broker protocol externally."),
      Spacer(),

      // ---------- 5. Queue tech ----------
      H1("5. Shared Queue Technology"),
      CompareTable(
        ["", "NATS JetStream", "RabbitMQ (Quorum)", "Postgres + SKIP LOCKED"],
        [
          ["Resources", "Single binary, no JVM", "Erlang VM, heavier", "No extra component — the same existing database"],
          ["HA", "Built-in Raft", "Raft (Quorum Queues)", "HA of the database itself only"],
          ["Anti-duplicate claim", "pull + ack/nak + MaxDeliver", "DLX + TTL", "SELECT FOR UPDATE SKIP LOCKED"],
          ["When it fits", "Default choice — lightweight, real HA, fits a work-queue", "Existing AMQP experience / rich routing needs", "Want no additional component at all"],
        ], [1600, 2600, 2600, 2200]
      ),
      Spacer(120),
      Callout("Conclusion", "NATS + JetStream, work-queue retention, 3 nodes. Kafka is explicitly rejected — built for event streaming at massive scale, not task dispatch (Shopify solves a problem like ours with SQS; Cloudflare uses Kafka for a very different problem — fanning out a trillion messages across teams, not dispatch-with-retry)."),
      Spacer(),

      // ---------- 6. File delivery — OPEN ----------
      H1("6. Print File Delivery — Still Open"),
      P("Option 1: the client attaches the file directly (multipart). Option 2: the client sends only a reference to a location where the file is stored, and the server fetches it."),
      Callout(
        "Recommendation",
        "Support both: direct attachment as the default up to about 10MB (covers the large majority of real print documents); above that threshold, or for high-volume internal clients, a reference (presigned URL) to S3-compatible storage (MinIO if on-prem). Important: most Windows servers have no S3/MinIO SDK, so direct attachment must remain the primary path, not just an option. If URL reference is supported, SSRF protection is required (see section 11).",
      ),
      Spacer(),

      // ---------- 7. Request schema ----------
      H1("7. Print Request Structure"),
      CompareTable(
        ["Field", "Required?", "Note"],
        [
          ["Printer ID", "Yes", "A logical ID from the catalog (section 13), not an IP address"],
          ["File / file reference", "One of the two", "See section 6"],
          ["Page size, resolution, color, page range, copies", "No", "Defaults to the printer's own defaults"],
          ["Priority level", "No", "See section 12"],
          ["Callback URL", "No", "Completion notification, an alternative to polling"],
          ["Idempotency key", "Yes", "Prevents a duplicate print on retry — see section 10"],
          ["Requester ID + trace_id", "Yes", "See section 8"],
        ], [2200, 1300, 5500]
      ),
      Spacer(),

      // ---------- 8. Logging + Audit ----------
      H1("8. Logging and Audit"),
      P("Two separate destinations: an operational log to Kibana/ELK (for engineers — debugging, dashboards, alerting), and an Audit trail in a dedicated database (accountability/compliance — “who printed what and when”). This is a standard industry separation (e.g. AWS CloudTrail vs. CloudWatch Logs), not something we invented."),
      H2("8.1 Audit record fields"),
      CompareTable(
        ["Field", "Note"],
        [
          ["actor / action / target", "Who performed it, which action, on which job/printer"],
          ["timestamp (UTC), result", "Includes an exact reason code, not just a boolean"],
          ["trace_id", "Shared between the operational log and the audit trail — W3C Trace Context / OpenTelemetry standard"],
          ["source_service, request_ip, sequence_number", "For tracing and chain verification"],
        ], [3000, 6000]
      ),
      Spacer(120),
      Bullet("Audit is append-only (no UPDATE/DELETE grants at the database level) plus a hash chain between records, plus an automated (daily) process that verifies the chain hasn't been broken."),
      Bullet("Separate access permissions between the Audit store and the operational log store."),
      CompareTable(
        ["", "Operational log (Kibana)", "Audit (dedicated DB)"],
        [
          ["Retention", "30-90 days", "Significantly longer — driven by compliance requirements"],
          ["Mutable", "Yes", "No — append-only"],
        ], [1800, 3600, 3600]
      ),
      Spacer(),

      // ---------- 9. Failure handling ----------
      H1("9. Failure Handling and Retry Policy"),
      CompareTable(
        ["Failure type", "Example", "Handling"],
        [
          ["Transient", "Printer busy, momentary network fault", "Automatic retry, with growing backoff between attempts"],
          ["Permanent", "Printer doesn't exist, corrupt file", "Fail immediately + alert, don't waste retries"],
          ["Ambiguous", "Connection closed after send, before ack", "Check actual status against CUPS/the printer before retrying"],
        ], [2000, 3400, 3600]
      ),
      Spacer(120),
      Callout("Conclusion", "A bounded number of retries (3-5). After exhausting them — a DLQ with full details, that someone actually reviews and gets alerted on, not a pile of logs nobody watches."),
      Spacer(),

      // ---------- 10. Crash recovery ----------
      H1("10. Crash Recovery"),
      Bullet("A unique key (idempotency key) per request — before a retry, check whether a successful print already happened with the same key."),
      Bullet("A time-bounded claim (ack/nak in the queue, or SELECT FOR UPDATE SKIP LOCKED in the database) — if a worker crashes mid-job, the request automatically returns to the queue."),
      Bullet("Leader election is NOT required: stateless workers + idempotency key + time-bounded claim are sufficient — the queue/broker already provides that consensus internally. The one exception: a periodic task that must run exactly once — solved with a lock (advisory lock), not a separate leader-election component."),
      Bullet("A critical rule, different from the general “prefer guaranteed-at-least-once” principle: if a node crashes after the page has already come out of the printer but before it was marked complete — before retrying, actually check the job's status against cupsd/the printer. If it cannot be determined with certainty — do not print again, flag for manual review. A duplicate print on real paper is an irreversible waste, unlike a duplicate database record."),
      Bullet("Graceful shutdown: SIGTERM immediately flips readiness off, the node stops accepting new jobs, waits for in-flight jobs to finish up to a reasonable time bound, then exits."),
      Spacer(),

      // ---------- 11. Security ----------
      H1("11. Security"),
      H2("11.1 Service-to-service authentication"),
      CompareTable(
        ["Alternative", "When it fits"],
        [
          ["mTLS (recommended)", "Default — a relatively stable set of callers (Windows servers, containers)"],
          ["API keys", "Temporary solution only — a leaked key stays valid until manually revoked"],
          ["OAuth2 client-credentials", "If an organizational identity provider already exists and fine-grained authorization is needed"],
          ["SPIFFE/SPIRE", "Overkill for now for a relatively stable fleet; an internal CA (step-ca/Vault PKI) with short-lived certificates is enough"],
        ], [3000, 6000]
      ),
      Spacer(120),
      H2("11.2 Ghostscript and container hardening"),
      Bullet("Patched version only (>=10.03.1) — CVE-2024-29510 (-dSAFER bypass) and CVE-2023-36664 (critical RCE) are real bugs, not theoretical."),
      Bullet("Non-root with no shell, read-only filesystem, seccomp/AppArmor, dropped unnecessary capabilities, per-job resource limits, and no network access from the render process itself."),
      H2("11.3 SSRF (if URL-reference file delivery is supported, section 6)"),
      Bullet("An allowlist of approved sources only; independent DNS resolution and checking the resulting IP against a deny-list (internal networks, 169.254.169.254) before connecting; re-validating the IP at actual connection time to prevent DNS rebinding."),
      Spacer(),

      // ---------- 12. Priorities ----------
      H1("12. Priorities, Load, and Rate Limiting"),
      Bullet("A separate queue per printer — a stuck printer must not block requests to other printers."),
      Bullet("At least two priority tiers (urgent / normal)."),
      Bullet("Rate limiting per caller/service — so one “noisy” Windows server can't starve other callers on the same shared queue."),
      Spacer(),

      // ---------- 13. Printer catalog ----------
      H1("13. Printer Catalog and Onboarding"),
      P("A central catalog per printer: logical ID, address, PPD/capabilities, whether ippfix is required, and what's supported (resolution, page size, etc.)."),
      Bullet("A standing (not one-off) onboarding process for a new printer: capability probing, checking for known-issue patterns similar to the one already found, generating a static PPD — the same method already proven on the first printer."),
      Bullet("A distribution/reconciliation mechanism: a central source of truth plus a job that runs on every node (on startup and periodically) applying lpadmin configuration accordingly — so the assumption “any node can serve any printer” (section 3) is actually true in practice, not just an assumption."),
      Spacer(),

      // ---------- 14. Capacity ----------
      H1("14. Capacity Planning and Resource Budget"),
      CompareTable(
        ["Component", "Per-node budget (estimate)"],
        [
          ["Gateway/Worker (excluding CUPS)", "0.5-1 vCPU, 256-512MB"],
          ["CUPS + ippfix + Ghostscript", "1-2 vCPU, 512MB-1GB, depending on concurrent renders"],
          ["NATS JetStream node (3-node cluster)", "~0.5 vCPU, under 300MB"],
        ], [4000, 5000]
      ),
      Spacer(),

      // ---------- 15. Monitoring / GUI ----------
      H1("15. Monitoring and Print Status Visibility"),
      H2("15.1 What CUPS already provides out of the box"),
      P("CUPS has a built-in web interface (port 631: /printers, /jobs, /admin, /classes) — showing the printer list and status, job list/history, and the ability to pause/cancel. It's generated directly by cupsd itself, not a separate service."),
      Bullet("Limitation 1 — no cross-node aggregation: every page is generated purely from that host's own cupsd data. CUPS has no built-in mechanism to merge or aggregate status across several separate cupsd instances."),
      Bullet("Limitation 2 — exposure: the default install listens on localhost only (127.0.0.1:631). Exposing it beyond that requires explicit changes at two levels: the Listen/Port directive (which address cupsd binds to) and <Location> blocks (who is allowed to connect once traffic arrives)."),
      Bullet("Limitation 3 — permissions: authentication is basic-auth via PAM, and administrative actions are gated on lpadmin group membership only — there is no real role/permission model (RBAC)."),
      Bullet("Limitation 4 — intended purpose: the industry consistently treats it as a per-node admin/debug tool, not an interface meant for broad exposure — official guidance for remote access recommends a VPN/SSH tunnel, not direct network exposure."),
      Spacer(),
      H2("15.2 What this means for our architecture (multiple nodes)"),
      P("Because we have several independent cupsd instances, but also one shared database (section 8) that already knows the real status across nodes and printers — CUPS's raw web interface is not a fit as the primary status view. This matches exactly how Microsoft Universal Print (a centralized view in the Azure portal, not a per-Connector UI) and PaperCut (a centralized dashboard in the Application Server, not a per-Print-Provider-node UI) solve the same problem shape."),
      Callout("Conclusion", "CUPS's web interface stays at its default — localhost only, reachable only via an SSH tunnel for a specific engineer's node-level debugging. It is never the view exposed to stakeholders."),
      Spacer(),
      H2("15.3 How print status is actually viewed"),
      P("Build a lightweight status view/API inside the Gateway service itself, reading from the same shared database (section 8) — aggregated across all nodes and all printers: how many requests in each status per printer, and online/offline status per printer (periodic polling via IPP, or using cups-browsed's own status). This is also the concrete answer to the “per-printer health check” requirement mentioned earlier."),
      Spacer(),
      H2("15.4 How to expose this information externally — still open"),
      P("This decision depends on what your organization already has in place, so it remains open:"),
      CompareTable(
        ["", "Option 1: Kibana dashboard", "Option 2: Prometheus + Grafana"],
        [
          ["Advantage", "Zero new infrastructure — uses the same operational log pipeline already in place (section 8)", "Purpose-built for numeric metrics/alerting — the standard industry choice for infrastructure monitoring"],
          ["Disadvantage", "Less suited to precise numeric metrics/alerting", "A new stack to run and maintain if not already present"],
          ["When it fits", "No Prometheus/Grafana yet for other services in the org", "The org has already standardized on Prometheus/Grafana elsewhere"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout(
        "Recommendation",
        "Option 1 by default (zero additional infrastructure). If Prometheus/Grafana is already the organization's standard for other services, Option 2 is preferable — in which case it's worth knowing that real, maintained open-source CUPS exporters already exist (e.g. phin1x/cups_exporter, which pulls data directly via IPP). Important: such an exporter only sees its own local cupsd — for our multi-node architecture, it's better to pull metrics directly from the shared database (section 8) rather than from each cupsd separately, to get an accurate cross-node picture.",
      ),
      Spacer(),

      // ---------- 16. Failure modes ----------
      H1("16. Failure Mode Map and Mitigation Plan"),
      CompareTable(
        ["Possible failure", "Impact", "Mitigation"],
        [
          ["A node crashes after the page has left the printer, before being marked complete", "An actual duplicate physical print", "Check status against cupsd/the printer before retrying; when uncertain, do not print again (section 10)"],
          ["The job store/Audit database goes down", "The entire system halts", "Replication/cluster (section 3); dedicated monitoring + immediate alert; a clear error to the client, not silent swallowing"],
          ["All queue nodes go down simultaneously", "New requests stop being accepted", "R3 replication + quorum monitoring; alert already on a single node's failure"],
          ["A single caller sends at too high a rate", "Starves other callers", "Per-caller rate limiting (section 12)"],
          ["A malicious print file exploits a Ghostscript vulnerability", "Code execution on the node", "Container hardening (section 11.2) + ongoing version updates"],
          ["A URL reference is abused for SSRF", "Exposure of internal networks", "Allowlist + resolve-then-validate (section 11.3)"],
          ["A worker crashes without the reaper catching it in a reasonable time", "A job effectively lost", "Monitor maximum dwell time in “in progress” status, a separate alert from the regular DLQ"],
          ["Clocks are not synchronized across nodes", "Unreliable event ordering in logs/audit", "Mandatory NTP across all nodes (section 3)"],
        ], [2600, 3200, 3200]
      ),
      Spacer(),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("print-gateway-hld-foundation-en.docx", buf);
  console.log("written");
});
