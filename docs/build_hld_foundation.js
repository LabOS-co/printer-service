const fs = require("fs");
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
  TableOfContents, PageBreak, VerticalAlign, LevelFormat,
} = require("docx");

const FONT = "Arial";
const RTL = true;

// see skill: hebrew-docx-bidi — split mixed Hebrew/Latin text into segments,
// mark Latin segments with the run-level rightToLeft:false property (not a
// character inserted into the text) so Word never has to guess direction
// around embedded English/technical terms.
function splitBidiSegments(str) {
  if (typeof str !== "string") return [{ text: str, ltr: false }];
  const re = /\(?[A-Za-z0-9][A-Za-z0-9 _./+,:;()%'"=<>@#&*\-]*/g;
  const segments = [];
  let lastIndex = 0;
  let m;
  while ((m = re.exec(str)) !== null) {
    if (m.index > lastIndex) segments.push({ text: str.slice(lastIndex, m.index), ltr: false });
    segments.push({ text: m[0], ltr: true });
    lastIndex = m.index + m[0].length;
  }
  if (lastIndex < str.length) segments.push({ text: str.slice(lastIndex), ltr: false });
  return segments.length ? segments : [{ text: str, ltr: false }];
}
function Runs(text, style = {}) {
  if (typeof text !== "string") return [new TextRun({ text, ...style, font: FONT })];
  return splitBidiSegments(text).map((seg) => new TextRun({
    text: seg.text, ...style, font: FONT, rightToLeft: !seg.ltr,
  }));
}

function P(text, opts = {}) {
  const { bold, size, color, italic, alignment = AlignmentType.RIGHT, spacingAfter = 120, heading, spacingBefore } = opts;
  return new Paragraph({
    heading, bidirectional: RTL, alignment,
    spacing: { after: spacingAfter, before: spacingBefore || 0 },
    children: Runs(text, { bold, italics: italic, size, color }),
  });
}
function Bullet(text, opts = {}) {
  return new Paragraph({
    bidirectional: RTL, alignment: AlignmentType.RIGHT,
    numbering: { reference: "bullet-list", level: 0 },
    spacing: { after: 80 },
    children: Runs(text, { bold: opts.bold, size: opts.size }),
  });
}
function H1(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_1, bidirectional: RTL, alignment: AlignmentType.RIGHT,
    spacing: { before: 360, after: 160 },
    children: Runs(text, { bold: true, color: "1F4E78" }),
  });
}
function H2(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_2, bidirectional: RTL, alignment: AlignmentType.RIGHT,
    spacing: { before: 240, after: 120 },
    children: Runs(text, { bold: true, color: "2E74B5" }),
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
          new Paragraph({ bidirectional: RTL, alignment: AlignmentType.RIGHT, spacing: { after: 60 },
            children: Runs(label, { bold: true, color: borderColor, size: 22 }) }),
          new Paragraph({ bidirectional: RTL, alignment: AlignmentType.RIGHT,
            children: Runs(text, { size: 21 }) }),
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
      children: [new Paragraph({ bidirectional: RTL, alignment: AlignmentType.RIGHT,
        children: Runs(h, { bold: true, color: "FFFFFF", size: 21 }) })],
    })),
  });
  const bodyRows = rows.map((r) => new TableRow({
    children: r.map((cellText, i) => new TableCell({
      width: { size: w[i], type: WidthType.DXA },
      verticalAlign: VerticalAlign.CENTER,
      margins: { top: 100, bottom: 100, left: 120, right: 120 },
      children: (Array.isArray(cellText) ? cellText : [cellText]).map((t) => new Paragraph({
        bidirectional: RTL, alignment: AlignmentType.RIGHT, spacing: { after: 40 },
        children: Runs(t, { size: 20 }),
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
      levels: [{ level: 0, format: LevelFormat.BULLET, text: "•", alignment: AlignmentType.RIGHT,
        style: { paragraph: { indent: { right: 420, hanging: 260 } } } }],
    }],
  },
  styles: {
    default: {
      document: { run: { font: FONT, size: 21 }, paragraph: { bidirectional: RTL } },
      heading1: { run: { font: FONT, bold: true, size: 30, color: "1F4E78" }, paragraph: { bidirectional: RTL, spacing: { before: 360, after: 160 } } },
      heading2: { run: { font: FONT, bold: true, size: 25, color: "2E74B5" }, paragraph: { bidirectional: RTL, spacing: { before: 240, after: 120 } } },
    },
  },
  sections: [{
    properties: { page: { size: { width: 11906, height: 16838 } } },
    children: [
      // ---------- Title page ----------
      new Paragraph({ spacing: { before: 1200, after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "מסמך מסקנות — תשתית לאיפיון מפורט", bold: true, size: 52, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: Runs("שרת הדפסה מרכזי — Print Gateway / Worker Service", { size: 26, color: "2E74B5" }) }),
      new Paragraph({ spacing: { after: 80 }, alignment: AlignmentType.CENTER,
        children: Runs("LAB-16894", { size: 24 }) }),
      Callout(
        "מטרת המסמך",
        "מסמך זה קובע, בצורה סופית וברורה, מה אנחנו רוצים לבנות — הוא משמש תשתית ישירה לכתיבת ה-HLD. כל נושא שיש בו החלטה — כתובה כמסקנה, בלי אפשרויות. במקומות הבודדים שבהם ההחלטה עדיין לא נסגרה בפועל (סימון “עדיין פתוח”), מובאות האפשרויות עם המלצה מתחתן. הבסיס הטכני (CUPS + IPP + ippfix) כבר הוכח בפועל ומודפס עליו פיזית — זו לא עוד נקודת החלטה.",
        "FFF2CC", "BF8F00"
      ),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- TOC ----------
      H1("תוכן עניינים"),
      new TableOfContents("תוכן עניינים", { hyperlink: true, headingStyleRange: "1-2" }),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- 1. Architecture ----------
      H1("1. ארכיטקטורה כללית"),
      P("שרשרת הפעולות המלאה, מהלקוח ועד הנייר:"),
      CodeBox([
        "1. לקוח (שרת Windows / container אחר)  →  REST API של ה-Gateway",
        "2. Gateway  →  שומר את הבקשה בתור המשותף + במסד הנתונים, מחזיר אישור מיידי",
        "3. Worker (על אחד מה-nodes)  →  שולף בקשה ממתינה מהתור",
        "4. Worker  →  מוסר את הקובץ ל-CUPS המקומי (דרך ippfix) ב-IPP",
        "5. CUPS מקומי  →  מדפיס בפועל למדפסת הפיזית",
        "6. Gateway  →  מעדכן סטטוס סופי (polling מהלקוח / callback)",
      ]),
      Spacer(80),
      P("שלושה מופעים זהים (nodes) של Gateway+Worker+CUPS+ippfix רצים במקביל, חסרי-מצב (stateless) — כל המצב האמיתי (אילו בקשות קיימות, מה הסטטוס שלהן) יושב בתור המשותף ובמסד הנתונים, לא בזיכרון של node ספציפי."),
      Spacer(),

      // ---------- 2. Print engine ----------
      H1("2. מנוע ההדפסה — CUPS + IPP + ippfix"),
      P("זו החלטה סגורה, לא נושא לדיון מחדש: מנוע ההדפסה הוא CUPS על לינוקס, מדבר IPP ישירות עם המדפסות, עם שני רכיבי עזר משלנו:"),
      Bullet("ippfix — פרוקסי הפוך קטן שמתקן שדות IPP שבורים שמדפסות מסוימות מחזירות (באג RFC 8011 שכבר אובחן ותוקן בפועל), כדי ש-CUPS יוכל בכלל לדבר איתן."),
      Bullet("PPD סטטי — “כרטיס יכולות” של המדפסת נוצר פעם אחת מראש (דרך ippfix), במקום להיבנות מחדש בכל הדפסה — נמנע תלות בניהול-משא-ומתן חי מול מדפסת שעלול להיכשל."),
      P("נתוני ביצועים אמיתיים שנמדדו (700 מול 60 בקשות, אותו קובץ): קבלת בקשה מהירה פי כ-54 מ-Windows Spooler+SumatraPDF (כי CUPS מחזיר אישור ברגע שהבקשה נכנסה לתור, ומרנדר ברקע), ו-Ghostscript זול פי כ-5 ב-CPU ופי כ-17 בזיכרון שיא לעומת SumatraPDF פר-הדפסה. מסלול Windows Spooler/SumatraPDF אינו בשימוש בפתרון זה."),
      Spacer(),

      // ---------- 3. HA ----------
      H1("3. זמינות גבוהה וטופולוגיה"),
      P("3 מופעים זהים (מינימום 2 לגיבוי, אך 3 מומלץ — מתיישר עם ה-quorum של שכבת התור, ר׳ סעיף 5). כל מופע stateless."),
      Bullet("מסד הנתונים (תור העבודות + Audit) חייב לרוץ בעצמו ב-HA (replication/cluster, למשל Postgres) — בלי זה, ה-N+1 ברמת ה-Gateway לא באמת נותן זמינות, כי המסד עצמו הופך לנקודת כשל יחידה."),
      Bullet("סנכרון שעונים (NTP) בין כל ה-nodes — נדרש כדי שסדר האירועים בלוג ובאודיט יהיה אמין."),
      P("קיימות שתי שכבות HA נפרדות, וכל אחת פותרת סוג כשל אחר:"),
      CompareTable(
        ["", "שכבת CUPS (per-node)", "שכבת תור העבודות"],
        [
          ["מה זה פותר", "מדפסת לא נגישה מנקודת מבט של node מסוים", "כל ה-node קרס או נתקע לפני שסיים לטפל בבקשה"],
          ["איך ממומש", "כל node מריץ CUPS מקומי + תור זהה לאותה מדפסת; cups-browsed (מנגנון implicitclass מובנה ב-CUPS) בוחר איזה עותק פנוי ומנתב אליו", "בקשה “ננעלת” לזמן מוגבל; אם לא הושלמה — חוזרת אוטומטית לתור, ל-worker אחר"],
          ["מה זה לא פותר", "container שנופל כולו — cups-browsed נופל איתו", "לא מונע לבד הדפסה כפולה פיזית (ר׳ סעיף 10)"],
        ],
        [1800, 3600, 3600]
      ),
      Spacer(160),
      Callout("מסקנה", "שתי השכבות פועלות יחד. אין ל-CUPS clustering מובנה בין nodes — זה בדיוק הדפוס שגם Microsoft Universal Print וגם PaperCut מיישמים: מצב מרכזי אחד + נקודות הדפסה מקומיות."),
      Spacer(),

      // ---------- 4. Intake channel — OPEN ----------
      H1("4. ערוץ קליטת בקשות — עדיין פתוח"),
      H2("4.1 בקשה סינכרונית מול תור אסינכרוני"),
      CompareTable(
        ["", "אפשרות 1: בקשה ישירה (סינכרונית)", "אפשרות 2: תור (אסינכרוני)"],
        [
          ["יתרונות", "פשוט להטמעה", "לקוח לא נחסם; סופג עומסים; ניסיון חוזר אוטומטי"],
          ["חסרונות", "לקוח מחכה זמן לא ידוע; תקלת מדפסת תוקעת את הבקשה", "מורכבות נוספת (סטטוס + בדיקה)"],
          ["מתי מתאים", "נפח נמוך מאוד, לקוח בודד", "יותר מלקוח אחד או מדפסת אחת — המצב שלנו"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout("המלצה", "אפשרות 2 — תור. כלפי הלקוח זה עדיין נראה כמו REST רגיל (POST להגשה + GET לסטטוס); התור פנימי בלבד."),
      Spacer(160),
      H2("4.2 REST בלבד מול חשיפת התור ישירות"),
      P("אפשרות 1: ה-REST API הוא החוזה החיצוני היחיד, התור פנימי לגמרי. אפשרות 2: לחשוף גם ערוץ תור ישיר לקוראים פנימיים מתקדמים."),
      Callout("המלצה", "אפשרות 1 כברירת מחדל — קורא Windows לא צריך ספריית תור כלל, רק HTTP. גם הנורמה בתעשייה: כמעט אין חברה גדולה שחושפת פרוטוקול broker גולמי כלפי חוץ."),
      Spacer(),

      // ---------- 5. Queue tech ----------
      H1("5. טכנולוגיית התור המשותף"),
      CompareTable(
        ["", "NATS JetStream", "RabbitMQ (Quorum)", "Postgres + SKIP LOCKED"],
        [
          ["משאבים", "בינארי בודד, ללא JVM", "Erlang VM, כבד יותר", "אין רכיב נוסף — אותו מסד קיים"],
          ["HA", "Raft מובנה", "Raft (Quorum Queues)", "HA של המסד עצמו בלבד"],
          ["claim נגד כפילות", "pull + ack/nak + MaxDeliver", "DLX + TTL", "SELECT FOR UPDATE SKIP LOCKED"],
          ["מתי מתאים", "ברירת מחדל — קליל, HA אמיתי, מתאים ל-work-queue", "ניסיון AMQP קיים / ניתוב עשיר", "לא רוצים רכיב נוסף כלל"],
        ], [1600, 2600, 2600, 2200]
      ),
      Spacer(120),
      Callout("מסקנה", "NATS + JetStream, retention מסוג work-queue, 3 nodes. Kafka נשלל — מיועד ל-event streaming בהיקף עצום, לא ל-dispatch של משימות (Shopify פותר בעיה כמו שלנו עם SQS; Cloudflare משתמש ב-Kafka לבעיה שונה לגמרי — הפצת טריליון הודעות בין צוותים, לא dispatch עם retry)."),
      Spacer(),

      // ---------- 6. File delivery — OPEN ----------
      H1("6. מסירת קובץ ההדפסה — עדיין פתוח"),
      P("אפשרות 1: הלקוח מצרף את הקובץ ישירות (multipart). אפשרות 2: הלקוח שולח רק הפניה לכתובת שבה הקובץ שמור, והשרת מביא אותו."),
      Callout(
        "המלצה",
        "לתמוך בשתיהן: צירוף ישיר כברירת מחדל עד כ-10MB (מכסה את רוב מסמכי ההדפסה בפועל); מעל לסף, או ללקוחות פנימיים בנפח גבוה — הפניה לכתובת (presigned URL) לאחסון תואם-S3 (MinIO אם on-prem). חשוב: לרוב שרתי Windows אין SDK ל-S3/MinIO, אז צירוף ישיר חייב להישאר הנתיב הראשי, לא רק אופציה. אם נתמכת הפניה לכתובת — נדרשת הגנת SSRF (ר׳ סעיף 11).",
      ),
      Spacer(),

      // ---------- 7. Request schema ----------
      H1("7. מבנה בקשת ההדפסה"),
      CompareTable(
        ["שדה", "חובה?", "הערה"],
        [
          ["מזהה מדפסת", "כן", "מזהה לוגי מהקטלוג (סעיף 13), לא כתובת IP"],
          ["הקובץ / הפניה לקובץ", "אחד מהשניים", "ר׳ סעיף 6"],
          ["גודל דף, רזולוציה, צבע, טווח עמודים, עותקים", "לא", "ברירת מחדל לפי המדפסת"],
          ["רמת עדיפות", "לא", "ר׳ סעיף 12"],
          ["כתובת callback", "לא", "התראה בסיום, חלופה ל-polling"],
          ["מפתח ייחודי (idempotency key)", "כן", "מונע הדפסה כפולה בניסיון חוזר — ר׳ סעיף 10"],
          ["מזהה מבקש + trace_id", "כן", "ר׳ סעיף 8"],
        ], [2200, 1300, 5500]
      ),
      Spacer(),

      // ---------- 8. Logging + Audit ----------
      H1("8. לוגים ו-Audit"),
      P("שתי יעדים נפרדים: לוג תפעולי ל-Kibana/ELK (למהנדסים — דיבוג, דשבורדים, התרעות), ו-Audit trail במסד נתונים ייעודי (בקרה/ציות — “מי הדפיס מה ומתי”). זו הפרדה סטנדרטית בתעשייה (למשל AWS CloudTrail מול CloudWatch Logs), לא המצאה."),
      H2("8.1 שדות רשומת Audit"),
      CompareTable(
        ["שדה", "הערה"],
        [
          ["actor / action / target", "מי ביצע, איזו פעולה, על איזה Job/מדפסת"],
          ["timestamp (UTC), result", "כולל קוד סיבה מדויק, לא רק בוליאני"],
          ["trace_id", "משותף ללוג התפעולי ולאודיט — תקן W3C Trace Context / OpenTelemetry"],
          ["source_service, request_ip, sequence_number", "לצורך מעקב ואימות שרשרת"],
        ], [3000, 6000]
      ),
      Spacer(120),
      Bullet("Audit הוא append-only בלבד (אין הרשאת UPDATE/DELETE ברמת מסד הנתונים) + שרשור hash בין רשומות + תהליך אוטומטי (יומי) שמוודא שהשרשרת לא נשברה."),
      Bullet("הרשאות גישה נפרדות בין מאגר ה-Audit למאגר הלוג התפעולי."),
      CompareTable(
        ["", "לוג תפעולי (Kibana)", "Audit (DB ייעודי)"],
        [
          ["retention", "30–90 יום", "ארוך משמעותית — נגזר מדרישות ציות"],
          ["ניתן לשינוי", "כן", "לא — append-only"],
        ], [1800, 3600, 3600]
      ),
      Spacer(),

      // ---------- 9. Failure handling ----------
      H1("9. טיפול בכשלים ומדיניות ניסיון חוזר"),
      CompareTable(
        ["סוג כשל", "דוגמה", "טיפול"],
        [
          ["זמני", "מדפסת עסוקה, תקלת רשת רגעית", "ניסיון חוזר אוטומטי, המתנה גדלה בין ניסיונות"],
          ["קבוע", "מדפסת לא קיימת, קובץ פגום", "כישלון מיידי + התראה, בלי לבזבז ניסיונות"],
          ["לא ברור", "החיבור נסגר אחרי שליחה, לפני אישור", "בדיקה בפועל מול CUPS/המדפסת לפני ניסיון נוסף"],
        ], [2000, 3400, 3600]
      ),
      Spacer(120),
      Callout("מסקנה", "מספר ניסיונות מוגבל (3–5). אחרי מיצוי — DLQ עם כל הפרטים, שמישהו בפועל בודק ומקבל עליו התראה, לא ערימת לוגים ללא מעקב."),
      Spacer(),

      // ---------- 10. Crash recovery ----------
      H1("10. התאוששות מנפילות"),
      Bullet("מפתח ייחודי (idempotency key) לכל בקשה — לפני ניסיון חוזר בודקים אם כבר בוצעה הדפסה מוצלחת עם אותו מפתח."),
      Bullet("claim מוגבל בזמן (ack/nak בתור או SELECT FOR UPDATE SKIP LOCKED במסד) — אם worker קרס באמצע, הבקשה חוזרת אוטומטית לתור."),
      Bullet("Leader election אינו נדרש: workers חסרי-מצב + idempotency key + claim מוגבל בזמן מספיקים — התור/broker כבר מספק consensus מבפנים. יוצא דופן יחיד: משימה תקופתית שחייבת לרוץ פעם אחת בלבד — פותרים עם נעילה (advisory lock) ולא עם רכיב leader-election נפרד."),
      Bullet("כלל קריטי, שונה מהעיקרון הכללי של “עדיף בטוח יבוצע”: אם node קרס אחרי שהעמוד כבר יצא מהמדפסת אך לפני שסומן כהושלם — לפני ניסיון חוזר יש לבדוק בפועל את סטטוס ה-job מול cupsd/המדפסת. אם אי אפשר לקבוע בוודאות — לא להדפיס שוב, לסמן לבדיקה ידנית. הדפסה כפולה על נייר אמיתי היא בזבוז בלתי הפיך, בשונה מרישום כפול במסד."),
      Bullet("כיבוי מסודר: SIGTERM מוריד את ה-readiness מיידית, ה-node מפסיק לקבל jobs חדשים, ממתין לסיום jobs שכבר בטיפול עד גבול זמן סביר, ואז יוצא."),
      Spacer(),

      // ---------- 11. Security ----------
      H1("11. אבטחה"),
      H2("11.1 אימות שירות-לשירות"),
      CompareTable(
        ["חלופה", "מתי מתאימה"],
        [
          ["mTLS (מומלץ)", "ברירת מחדל — קבוצת קוראים יציבה יחסית (שרתי Windows, containers)"],
          ["מפתחות API", "פתרון זמני בלבד — מפתח שדלף תקף עד ביטול ידני"],
          ["OAuth2 client-credentials", "אם קיים כבר ספק זהות ארגוני ונדרשת בקרת הרשאות עדינה"],
          ["SPIFFE/SPIRE", "overkill כרגע לצי יציב; מספיק CA פנימי (step-ca/Vault PKI) עם אישורים לתקופה קצרה"],
        ], [3000, 6000]
      ),
      Spacer(120),
      H2("11.2 הקשחת Ghostscript ו-containers"),
      Bullet("גרסה מתוקנת בלבד (≥10.03.1) — CVE-2024-29510 (עקיפת -dSAFER), CVE-2023-36664 (RCE קריטי) הם באגים אמיתיים, לא תיאורטיים."),
      Bullet("non-root ללא shell, מערכת קבצים לקריאה בלבד, seccomp/AppArmor, הסרת capabilities מיותרים, הגבלת משאבים פר-job, ואיסור גישה לרשת מתהליך ה-render עצמו."),
      H2("11.3 SSRF (אם נתמכת הפניה לכתובת, סעיף 6)"),
      Bullet("allowlist של מקורות מאושרים בלבד; resolve עצמאי של DNS ובדיקת ה-IP מול רשימת כתובות אסורות (רשתות פנימיות, 169.254.169.254) לפני חיבור; אימות חוזר על ה-IP בזמן החיבור עצמו למניעת DNS rebinding."),
      Spacer(),

      // ---------- 12. Priorities ----------
      H1("12. עדיפויות, עומסים והגבלת קצב"),
      Bullet("תור נפרד לכל מדפסת — מדפסת תקועה לא עוצרת בקשות למדפסות אחרות."),
      Bullet("שתי רמות עדיפות לפחות (דחוף / רגיל)."),
      Bullet("הגבלת קצב (rate limiting) פר-קורא/שירות — כדי ששרת Windows “רועש” אחד לא יחניק קוראים אחרים על אותו תור משותף."),
      Spacer(),

      // ---------- 13. Printer catalog ----------
      H1("13. קטלוג מדפסות ואונבורדינג"),
      P("קטלוג מרכזי לכל מדפסת: מזהה לוגי, כתובת, PPD/יכולות, האם נדרש ippfix, ומה נתמך (רזולוציה, גודל דף וכו׳)."),
      Bullet("תהליך אונבורדינג קבוע (לא חד-פעמי) למדפסת חדשה: בדיקת יכולות, זיהוי תקלות דומות לזו שכבר נמצאה, יצירת PPD סטטי — אותה שיטה שכבר עבדה על המדפסת הראשונה."),
      Bullet("מנגנון הפצה/reconciliation: מקור אמת מרכזי + job שרץ בכל node (בעלייה ובאופן תקופתי) שמיישם lpadmin בהתאם — כדי שההנחה “כל node יכול לשרת כל מדפסת” (סעיף 3) תהיה נכונה בפועל, לא רק הנחת יסוד."),
      Spacer(),

      // ---------- 14. Capacity ----------
      H1("14. תכנון קיבולת ותקציב משאבים"),
      CompareTable(
        ["רכיב", "תקציב פר-node (הערכה)"],
        [
          ["Gateway/Worker (ללא CUPS)", "0.5–1 vCPU, 256–512MB"],
          ["CUPS + ippfix + Ghostscript", "1–2 vCPU, 512MB–1GB, תלוי במקביליות renders"],
          ["NATS JetStream node (אשכול 3)", "~0.5 vCPU, מתחת ל-300MB"],
        ], [4000, 5000]
      ),
      Spacer(),

      // ---------- 15. Monitoring / GUI ----------
      H1("15. ניטור ותצוגת מצב הדפסה"),
      H2("15.1 מה כבר קיים בתוך CUPS עצמו"),
      P("ל-CUPS יש ממשק web מובנה (פורט 631: /printers, /jobs, /admin, /classes) — מציג רשימת מדפסות וסטטוס, רשימת/היסטוריית עבודות, ואפשרות להשהות/לבטל. הוא נוצר ישירות על ידי cupsd עצמו, ולא רכיב נפרד."),
      Bullet("מגבלה 1 — אין צירוף בין nodes: כל דף נוצר מהמידע המקומי של אותו cupsd בלבד. אין ב-CUPS שום מנגנון מובנה לצרף/לאחד סטטוס בין כמה מופעי cupsd נפרדים."),
      Bullet("מגבלה 2 — חשיפה: ברירת המחדל מאזינה על localhost בלבד (127.0.0.1:631). חשיפה מעבר לכך דורשת שינוי מפורש בשתי רמות: directive‏ Listen/Port (איזו כתובת cupsd מאזין עליה) וגם בלוקים <Location> (מי מורשה להתחבר לאחר שהתעבורה כבר הגיעה)."),
      Bullet("מגבלה 3 — הרשאות: אימות הוא basic-auth דרך PAM, ואישור פעולות ניהוליות מבוסס חברות בקבוצת lpadmin בלבד — אין מודל תפקידים/הרשאות אמיתי (RBAC)."),
      Bullet("מגבלה 4 — ייעוד: בתעשייה הוא נחשב לכלי ניהול/דיבוג ברמת node בודד, לא ממשק שמיועד לחשיפה רחבה — ההנחיה המקובלת לגישה מרחוק היא VPN/מנהרת SSH, לא חשיפה ישירה לרשת."),
      Spacer(),
      H2("15.2 המסקנה עבור הארכיטקטורה שלנו (ריבוי nodes)"),
      P("כיוון שיש לנו כמה מופעי cupsd עצמאיים, אבל גם מסד נתונים משותף אחד (סעיף 8) שכבר יודע את הסטטוס האמיתי חוצה-nodes וחוצה-מדפסות — ממשק ה-web הגולמי של CUPS אינו מתאים כתצוגת המצב הראשית. זה בדיוק תואם איך Microsoft Universal Print (תצוגה מרוכזת ב-Azure portal, לא לכל Connector בנפרד) ו-PaperCut (דשבורד מרכזי ב-Application Server, לא לכל Print Provider node בנפרד) פותרים את אותה בעיה."),
      Callout("מסקנה", "ממשק ה-web של CUPS נשאר כברירת המחדל שלו — localhost בלבד, נגיש רק דרך מנהרת SSH לצורך דיבוג נקודתי ברמת node בודד על ידי מהנדס. הוא לעולם לא התצוגה שנחשפת לבעלי עניין."),
      Spacer(),
      H2("15.3 איך רואים את מצב ההדפסה בפועל"),
      P("נבנה תצוגת/API סטטוס קליל בתוך שירות ה-Gateway עצמו, שקורא מאותו מסד הנתונים המשותף (סעיף 8) — מרוכז על פני כל ה-nodes וכל המדפסות: כמה בקשות בכל סטטוס פר-מדפסת, ומצב מקוון/לא-מקוון פר-מדפסת (polling תקופתי דרך IPP או ניצול סטטוס cups-browsed). זה גם הפתרון הקונקרטי לדרישת “בדיקת תקינות פר-מדפסת” שהוזכרה בסעיפים קודמים."),
      Spacer(),
      H2("15.4 איך מוציאים את המידע הזה החוצה — עדיין פתוח"),
      P("זו החלטה שתלויה במה שכבר קיים אצלכם כארגון, לכן עדיין פתוחה:"),
      CompareTable(
        ["", "אפשרות 1: דשבורד Kibana", "אפשרות 2: Prometheus + Grafana"],
        [
          ["יתרון", "אפס תשתית חדשה — משתמש באותו צינור לוגים תפעוליים שכבר קיים (סעיף 8)", "בנוי במיוחד למדדים מספריים/התרעות — הבחירה הסטנדרטית בתעשייה לניטור תשתית"],
          ["חיסרון", "פחות מותאם למדדים מספריים/התרעות מדויקות", "מחסנית חדשה להרצה ולתחזוקה אם לא קיימת כבר בארגון"],
          ["מתי מתאים", "אין עדיין Prometheus/Grafana בארגון לשירותים אחרים", "הארגון כבר סטנדרטיזציה על Prometheus/Grafana במקומות אחרים"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout(
        "המלצה",
        "אפשרות 1 כברירת מחדל (אפס תשתית נוספת). אם בפועל יש כבר Prometheus/Grafana סטנדרטי בארגון לשירותים אחרים — עדיף אפשרות 2, ואז שווה לדעת שקיימים exporters קוד-פתוח מתוחזקים ל-CUPS (למשל phin1x/cups_exporter, שואב דרך IPP ישירות). חשוב: exporter כזה רואה רק את ה-cupsd המקומי שלו — לארכיטקטורה שלנו (ריבוי nodes) עדיף לשאוב מדדים מהמסד המשותף (סעיף 8) ישירות ולא מכל cupsd בנפרד, כדי לקבל תמונה מדויקת חוצה-nodes.",
      ),
      Spacer(),

      // ---------- 16. Failure modes ----------
      H1("16. מיפוי כשלים אפשריים ותוכנית טיפול"),
      CompareTable(
        ["כשל אפשרי", "השפעה", "טיפול"],
        [
          ["node קורס אחרי שהעמוד יצא מהמדפסת, לפני שסומן כהושלם", "הדפסה כפולה בפועל", "בדיקת סטטוס מול cupsd/המדפסת לפני retry; באי-ודאות — לא להדפיס שוב (סעיף 10)"],
          ["מסד ה-Job store/Audit נופל", "כל המערכת נעצרת", "replication/cluster (סעיף 3); ניטור + התרעה מיידית; שגיאה ברורה ללקוח, לא בליעה שקטה"],
          ["כל nodes התור נופלים בו-זמנית", "הפסקת קליטת בקשות", "R3 replication + ניטור quorum; התרעה כבר בנפילת node בודד"],
          ["קורא בודד שולח בקצב גבוה מדי", "הרעבת קוראים אחרים", "rate limiting פר-caller (סעיף 12)"],
          ["קובץ זדוני מנצל חולשה ב-Ghostscript", "הרצת קוד על ה-node", "הקשחת containers (סעיף 11.2) + עדכוני גרסה שוטפים"],
          ["הפניה לכתובת מנוצלת ל-SSRF", "חשיפת רשתות פנימיות", "allowlist + resolve-then-validate (סעיף 11.3)"],
          ["worker קורס בלי שה-reaper תופס בזמן סביר", "job “אבוד” בפועל", "ניטור זמן שהייה מקסימלי ב“בטיפול”, התרעה נפרדת מה-DLQ"],
          ["שעונים לא מסונכרנים בין nodes", "סדר אירועים לא אמין בלוג/audit", "NTP חובה על כל ה-nodes (סעיף 3)"],
        ], [2600, 3200, 3200]
      ),
      Spacer(),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("print-gateway-hld-foundation.docx", buf);
  console.log("written");
});
