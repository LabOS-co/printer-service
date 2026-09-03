const fs = require("fs");
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
  TableOfContents, PageBreak, convertInchesToTwip, VerticalAlign,
  Numbering, LevelFormat,
} = require("docx");

const FONT = "Arial";
const RTL = true;

// Word's bidi (UAX#9) algorithm reorders "weak"/neutral characters (hyphens,
// slashes, parentheses, digits, dots) based on surrounding context. When an
// English/technical term sits inside a Hebrew RTL paragraph with no explicit
// direction marker, that context is ambiguous — the result is the visible
// jumbling (e.g. "CVE-2024-29510" or "10.03.1" or "(per-node)" scrambling).
// Fix: split the text into alternating Hebrew/Latin segments and mark each
// Latin segment with the OOXML run-level property rightToLeft:false
// (<w:rtl w:val="0"/> in w:rPr). This is pure formatting metadata, not a
// character inserted into the text — unlike Unicode bidi control characters
// (LRM/RLM/LRI/PDI), it has no visible glyph under any circumstance, so it
// can't render as a stray box/tag the way the control-character approach
// did the first time around.
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

// builds an array of TextRun objects from one logical string, applying the
// same run-level style to every segment but toggling rightToLeft per segment
function Runs(text, style = {}) {
  if (typeof text !== "string") return [new TextRun({ text, ...style, font: FONT })];
  return splitBidiSegments(text).map((seg) => new TextRun({
    text: seg.text,
    ...style,
    font: FONT,
    rightToLeft: !seg.ltr,
  }));
}

function P(text, opts = {}) {
  const { bold, size, color, italic, alignment = AlignmentType.RIGHT, spacingAfter = 120, heading, spacingBefore } = opts;
  return new Paragraph({
    heading,
    bidirectional: RTL,
    alignment,
    spacing: { after: spacingAfter, before: spacingBefore || 0 },
    children: Runs(text, { bold, italics: italic, size, color }),
  });
}

function Bullet(text, opts = {}) {
  return new Paragraph({
    bidirectional: RTL,
    alignment: AlignmentType.RIGHT,
    numbering: { reference: "bullet-list", level: 0 },
    spacing: { after: 80 },
    children: Runs(text, { bold: opts.bold, size: opts.size }),
  });
}

function H1(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_1,
    bidirectional: RTL,
    alignment: AlignmentType.RIGHT,
    spacing: { before: 360, after: 160 },
    children: Runs(text, { bold: true, color: "1F4E78" }),
  });
}

function H2(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_2,
    bidirectional: RTL,
    alignment: AlignmentType.RIGHT,
    spacing: { before: 240, after: 120 },
    children: Runs(text, { bold: true, color: "2E74B5" }),
  });
}

// callout / recommendation box — single-cell shaded table
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
    rows: [
      new TableRow({
        children: [
          new TableCell({
            shading: { type: ShadingType.CLEAR, fill: color },
            margins: { top: 150, bottom: 150, left: 200, right: 200 },
            children: [
              new Paragraph({
                bidirectional: RTL,
                alignment: AlignmentType.RIGHT,
                spacing: { after: 60 },
                children: Runs(label, { bold: true, color: borderColor, size: 22 }),
              }),
              new Paragraph({
                bidirectional: RTL,
                alignment: AlignmentType.RIGHT,
                children: Runs(text, { size: 21 }),
              }),
            ],
          }),
        ],
      }),
    ],
  });
}

function Spacer(h = 120) {
  return new Paragraph({ spacing: { after: h }, children: [] });
}

// comparison table: two options side by side
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
        bidirectional: RTL, alignment: AlignmentType.RIGHT,
        children: Runs(h, { bold: true, color: "FFFFFF", size: 21 }),
      })],
    })),
  });
  const bodyRows = rows.map((r) => new TableRow({
    children: r.map((cellText, i) => new TableCell({
      width: { size: w[i], type: WidthType.DXA },
      verticalAlign: VerticalAlign.CENTER,
      margins: { top: 100, bottom: 100, left: 120, right: 120 },
      children: (Array.isArray(cellText) ? cellText : [cellText]).map((t) => new Paragraph({
        bidirectional: RTL, alignment: AlignmentType.RIGHT,
        spacing: { after: 40 },
        children: Runs(t, { size: 20 }),
      })),
    })),
  }));
  return new Table({
    width: { size: total, type: WidthType.DXA },
    columnWidths: w,
    rows: [headerRow, ...bodyRows],
  });
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
          alignment: AlignmentType.LEFT,
          spacing: { after: 20 },
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
    properties: { page: { size: { width: 11906, height: 16838 } } }, // A4
    children: [
      // ---------- Title page ----------
      new Paragraph({ spacing: { before: 1200, after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "איפיון זמינות גבוהה, תורים ואבטחה", bold: true, size: 56, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: Runs("הרחבה לשרת ההדפסה המרכזי — Print Gateway / Worker Service", { size: 26, color: "2E74B5" }) }),
      new Paragraph({ spacing: { after: 80 }, alignment: AlignmentType.CENTER,
        children: Runs("LAB-16894", { size: 24 }) }),
      new Paragraph({ spacing: { after: 600 }, alignment: AlignmentType.CENTER,
        children: Runs("מבוסס על ה-POC שאומת פיזית (CUPS + IPP + ippfix) ועל האיפיון המקורי print-server-spec.docx", { size: 22, italics: true, color: "555555" }) }),
      Callout(
        "סטטוס המסמך",
        "מסמך זה — 2026-07-27 — עוסק רק בהרחבה: זמינות גבוהה (N+1), התור המשותף, קליטת בקשות מריבוי קוראים, מסירת קבצים, לוגים כפולים ו-Audit, אבטחה, והתאוששות מנפילות ב-HA. הוא אינו כולל את הרקע, הארכיטקטורה הבסיסית ושאר הסעיפים (1–13) מהאיפיון המקורי print-server-spec.docx — למי שצריך את אלה, זה עדיין המסמך המלא לרקע. כל מקום שבו יש יותר מדרך סבירה אחת מסומן בבירור כ“אפשרות 1 / אפשרות 2”, בלי הכרעה — לדיון משותף. בסוף המסמך (סעיף 8) יש רשימה מפורשת של דברים שלא נשאלנו עליהם ישירות אבל נוספו כי בלעדיהם הפתרון לא שלם.",
        "FFF2CC", "BF8F00"
      ),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- TOC ----------
      H1("תוכן עניינים"),
      new TableOfContents("תוכן עניינים", { hyperlink: true, headingStyleRange: "1-2" }),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- 1. HA architecture ----------
      H1("1. ריבוי שרתים וזמינות גבוהה (N+1)"),
      P("דרישה: יותר משרת (container) אחד, כדי שאם אחד נופל השני ימשיך לשרת, ושתיהם יחלקו אותו תור. המלצה: 3 מופעי Gateway/Worker זהים (מינימום 2 לגיבוי בלבד, אך 3 מומלץ — זה גם מספר ה-quorum הטבעי של Raft שמשמש את שכבת התור, ר׳ סעיף 2). כל מופע stateless מבחינת הלוגיקה שלו — כל המצב האמיתי (אילו jobs קיימים, מה הסטטוס שלהם) יושב בתור/במסד הנתונים, לא בזיכרון המופע."),
      P("הנקודה החשובה שעלתה מהחקירה: יש כאן בעצם שתי שכבות HA נפרדות ולא אחת, וכל אחת פותרת סוג כשל אחר. לבלבל ביניהן זו טעות ארכיטקטונית נפוצה:"),
      CompareTable(
        ["", "שכבת CUPS (per-node)", "שכבת תור העבודות (Broker/DB)"],
        [
          ["מה זה פותר", "המדפסת לא נגישה מנקודת המבט של node מסוים (queue מקומי מושהה, תקלת רשת נקודתית מה-node הזה)", "כל ה-node/worker קרס או נתקע לפני שסיים לטפל בבקשה"],
          ["איך ממומש", "כל node מריץ CUPS מקומי + תור זהה לאותה מדפסת (אותו שם); cups-browsed עם BrowsePoll בין ה-nodes בוחר אוטומטית איזה עותק פנוי ומנתב אליו (מנגנון CUPS מובנה בשם implicitclass — לא צריך להמציא אותו)", "בקשה “ננעלת” לזמן מוגבל בזמן טיפול; אם לא הושלמה בזמן — חוזרת אוטומטית לתור, ל-worker אחר (ר׳ סעיף 2)"],
          ["מה זה לא פותר", "לא עוזר אם ה-container/node כולו נופל — cups-browsed שרץ עליו נופל איתו", "לא מונע לבד הדפסה כפולה בפועל על הנייר אם ה-node קרס אחרי שהעמוד כבר יצא מהמדפסת (ר׳ סעיף 6)"],
        ],
        [1800, 3600, 3600]
      ),
      Spacer(160),
      Callout(
        "המלצה",
        "להשתמש בשתי השכבות יחד — הן משלימות, לא כפולות. אין ל-CUPS עצמו יכולת clustering מובנית בין nodes (אין “CUPS מרכזי” אחד) — זה מוצהר ומתועד. הדפוס הנכון, שגם Microsoft Universal Print (Connector שמקבל jobs מהתור המרכזי) וגם PaperCut (Application Server מרכזי + secondary print servers) מיישמים בדרכם: תור/מצב מרכזי אחד + מספר נקודות הדפסה מקומיות שמדברות בפועל עם המדפסת. cups-browsed/implicitclass הוא הדרך התקנית ב-CUPS עצמו לגרום למספר nodes להתנהג כמו “מדפסת אחת גמישה” ברמת ה-queue המקומי.",
      ),
      Spacer(),

      // ---------- 2. Shared queue technology ----------
      H1("2. התור המשותף — איזו טכנולוגיה"),
      P("דרישה: התור חייב להיות משותף בין כל מופעי ה-Gateway/Worker (לא תור נפרד לכל מופע), חסכוני ב-CPU/זיכרון, ועם ביצועים גבוהים. הושוו שלוש חלופות מציאותיות:"),
      CompareTable(
        ["", "NATS JetStream", "RabbitMQ (Quorum Queues)", "Postgres + SKIP LOCKED"],
        [
          ["טביעת רגל משאבים", "בינארי בודד, ללא תלות חיצונית (לא JVM/ZooKeeper); ריצה קרה ~כמה MB", "תלוי ב-Erlang VM; מומלץ יצרן ~1 vCPU/2GB גם לסביבת פיתוח קטנה", "אין רכיב נוסף כלל — משתמש במסד הנתונים שכבר קיים לצורך ה-Job store"],
          ["HA מובנה", "Raft, שכפול stream בין nodes (למשל R3)", "Quorum Queues — גם מבוסס Raft (יורש את התורים הישנים שכופלו כ-mirror)", "HA של המסד עצמו (ר׳ סעיף 8) — אין HA ברמת ה-“תור” בנפרד מה-DB"],
          ["מנגנון claim נגד כפילות", "pull consumer + ack/nak + MaxDeliver מובנה", "DLX (dead-letter exchange) + TTL מובנה", "SELECT ... FOR UPDATE SKIP LOCKED — נעילת שורה אטומית בין workers מתחרים"],
          ["עדיפויות/DLQ", "נתמך (advisory subjects → stream נפרד כ-DLQ)", "נתמך באופן טבעי (תור עדיפויות מובנה)", "אפשר לממש עם עמודת priority + ORDER BY, אך ידני"],
          ["מתי מתאים", "ברירת מחדל מומלצת — הכי חסכוני, מתאים בדיוק לתבנית “תור עבודות” (work-queue), לא event-streaming", "אם כבר יש ניסיון תפעולי עם AMQP או נדרש ניתוב עשיר בין תורים", "אם רוצים לא להוסיף רכיב חדש בכלל, ומוכנים לכתוב את לוגיקת ה-reaper בעצמכם"],
        ],
        [1600, 2600, 2600, 2200]
      ),
      Spacer(160),
      Callout(
        "המלצה",
        "NATS + JetStream כברירת מחדל, עם retention מסוג work-queue ו-3 nodes משוכפלים. זו הטכנולוגיה היחידה מבין השלוש שהיא בו-זמנית: (א) ללא תלות בסביבת ריצה כבדה (לא JVM), (ב) בעלת HA מובנה אמיתי (Raft), ו-(ג) בנויה מהיסוד ל-“תור עבודות מתחרים”, לא ל-streaming של אירועים. Apache Kafka נשלל בכוונה — הוא מיועד ל-streaming של אירועים בהיקף עצום, לא ל-dispatch של משימות; מודל ה-consumer-group שלו “מגלגל אחורה” partition שלם בכשל, לא רק את ה-job שנכשל, וגם ב-KRaft הוא כבד בהרבה (JVM, דיסק).",
      ),
      Spacer(160),
      P("הערה חשובה מהעולם האמיתי: Shopify משתמש ב-AWS SQS בדיוק לתבנית “תור עבודות” הזו (עדכוני מלאי, אישורי הזמנות) — הכי דומה לצורך שלנו. Cloudflare משתמש ב-Kafka, אבל לבעיה שונה בצורתה: הפצת כ-1 טריליון הודעות בין צוותים רבים (event fan-out), לא dispatch של job בודד עם retry. אלו לא שתי חברות שבחרו אחרת לאותה בעיה — אלו שתי בעיות שונות שנראות דומות מבחוץ. המסקנה: אל תבחרו Kafka רק כי הוא פופולרי — לבעיה שלנו (job דומה לזה שיש ל-Shopify) פתרון קליל כמו NATS/SQS הוא הבחירה הנכונה.", { size: 20 }),
      Spacer(),
      Callout(
        "החלטה פתוחה נלווית — איך קוראים חיצוניים ניגשים לתור",
        "אפשרות 1 (מומלץ): ה-REST API הוא החוזה החיצוני היחיד; NATS פנימי לגמרי, קורא חיצוני (שרת Windows וכו׳) אף פעם לא מדבר איתו ישירות. זו גם הנורמה בתעשייה — כמעט אף חברה גדולה לא חושפת פרוטוקול broker גולמי כלפי חוץ. יתרון נוסף: קורא Windows לא צריך ספריית NATS/AMQP כלל, רק HTTP רגיל. אפשרות 2: לחשוף גם ערוץ NATS ישיר לקוראים פנימיים/מתקדמים שכבר יודעים לדבר איתו (פחות תקורה, אבל מצמיד את הקוראים לבחירת ה-broker הפנימית שלנו).",
      ),
      Spacer(),

      // ---------- 3. File delivery refinement ----------
      H1("3. מסירת קובץ ההדפסה — קריטריון מספרי + הערת אבטחה"),
      P("האיפיון המקורי (print-server-spec.docx, סעיף 4) כבר קבע לתמוך גם בשליחה ישירה וגם בהפניה לקובץ. החקירה נתנה קריטריון מעשי להחלטה בפועל, ולא רק “שתיהן אפשריות”:"),
      Bullet("ברירת מחדל: צירוף ישיר (multipart) לכל קובץ עד בסביבות 10MB — זהו גם קו הגבול המקובל בפועל אצל ספקי API גדולים (לדוגמה מגבלת body ב-API Gateway של AWS), וגם מכסה את רוב מסמכי ההדפסה בפועל (חשבוניות/מכתבים — קילובייטים עד כמה MB)."),
      Bullet("מעל לסף הזה, או לקוחות פנימיים בנפח גבוה שכבר שומרים את הקובץ איפשהו: הפניה לכתובת (presigned URL) לאחסון תואם-S3 — ואם הפריסה היא on-prem/לא ענן, MinIO (תואם S3 API לחלוטין) הוא הפתרון הסטנדרטי, לא רק AWS S3."),
      Bullet("נקודה קריטית לגבי שרתי Windows כקוראים: לרבים מהם אין ולא יהיה SDK ל-S3/MinIO. לכן צירוף ישיר (multipart) חייב להישאר הנתיב הראשי והפשוט — לא לכפות על קורא Windows לדעת “לדבר S3” כתנאי סף."),
      Bullet("אם משתמשים בהפניה לכתובת: זה בדיוק המקום שבו עלול להיפתח פרצת SSRF (השרת שלנו הולך להוריד מכתובת שהלקוח נתן) — ר׳ הקשחה בסעיף 5.3."),
      Spacer(),

      // ---------- 4. Audit + dual logging ----------
      H1("4. Audit ולוגים כפולים"),
      P("החקירה מאששת: הפרדה בין audit trail (למסד נתונים ייעודי) לבין לוג תפעולי (ל-Kibana/פלטפורמה דומה) היא לא המצאה שלנו — זו הדרך המקובלת בתעשייה. הזוג המקביל אצל AWS הוא CloudTrail (audit) מול CloudWatch Logs (תפעולי); אצל Google זה Cloud Audit Logs מול Cloud Logging. שני החברות שומרות את זה כשני מאגרים נפרדים, לא מאוחדים, גם כשיש שיקוף חלקי ביניהם."),
      H2("4.1 רשומת Audit — שדות מומלצים"),
      CompareTable(
        ["שדה", "הערה"],
        [
          ["actor", "מי ביצע — מזהה קורא/שירות (לא רק “משתמש” — כאן ברוב המקרים זה שרת/שירות)"],
          ["action", "הפעולה: הגשת בקשה, שינוי סטטוס, ביטול, ניסיון חוזר וכו׳"],
          ["target", "מזהה ה-Job ומזהה המדפסת"],
          ["timestamp", "UTC — וגם זמן קבלה בפועל וגם, אם קיים, זמן מקור"],
          ["result", "הצלחה/כשל + קוד סיבה מדויק (לא רק בוליאני)"],
          ["correlation / trace_id", "ר׳ 4.3 — אותו מזהה חייב להופיע גם בלוג התפעולי"],
          ["source_service, request_ip", "מאיזה שרת/container הגיעה הבקשה בפועל"],
          ["sequence_number", "מספר סידורי לצורך אימות שרשרת (ר׳ 4.2)"],
        ],
        [2200, 6800]
      ),
      Spacer(160),
      H2("4.2 מניעת שיבוש (tamper-evidence) — מנגנונים בפועל"),
      Bullet("טבלת ה-audit היא append-only בלבד — לתפקיד/למשתמש שהאפליקציה כותבת דרכו אין הרשאת UPDATE/DELETE ברמת מסד הנתונים, לא רק ברמת קוד."),
      Bullet("שרשור hash — כל רשומה שומרת hash(הרשומה הקודמת + עצמה), כך ששינוי בדיעבד שובר את השרשרת; חשוב: תהליך אוטומטי (לדוגמה יומי) שעובר על השרשרת ומתריע אם היא נשברה — זה החלק שרוב הצוותים מדלגים עליו בטעות."),
      Bullet("הרשאות גישה נפרדות ומצומצמות יותר בין מאגר ה-audit לבין מאגר הלוג התפעולי — לא אותו משתמש/תפקיד DB."),
      Bullet("אופציה נוספת להקשחה (אם נדרש רמת עמידות גבוהה יותר): אחסון WORM ברמת האחסון עצמו (לדוגמה S3 Object Lock / Azure Immutable Blob), שהופך שינוי לבלתי אפשרי טכנית, לא רק ניתן לזיהוי."),
      Spacer(),
      H2("4.3 מתאם בין הלוג התפעולי ל-Audit — trace ID"),
      P("התקן שהתעשייה כבר התכנסה אליו הוא W3C Trace Context (כותרות traceparent/tracestate) דרך OpenTelemetry — לא רק “אפשרות אחת מכמה”. יש להנפיק trace_id אחד לכל בקשת הדפסה מרגע הקבלה, להעביר אותו בכל קפיצה בין containers/שירותים, ולהטביע אותו גם בלוג התפעולי (Kibana) וגם ברשומת ה-audit (DB) — כך אפשר לצלוב בין השניים בזמן חקירה, בלי לאחד אותם למאגר אחד."),
      Spacer(80),
      CompareTable(
        ["", "לוג תפעולי (Kibana/ELK)", "Audit trail (DB ייעודי)"],
        [
          ["קהל יעד", "מהנדסים — דיבוג, דשבורדים, התרעות", "בקרה/ציות/תחקור — “מי הדפיס מה ומתי”"],
          ["retention טיפוסי", "30–90 יום (לפעמים עד שנה), לפי עלות ותועלת דיבוג", "ארוך משמעותית — נגזר מדרישות רגולציה/ארגון, לא מעלות"],
          ["ניתן לשינוי", "כן, זה בסדר — נועד לניתוח, לא לראיה", "לא — append-only + שרשור hash"],
        ],
        [1800, 3600, 3600]
      ),
      Spacer(),

      // ---------- 5. Security ----------
      H1("5. אבטחה מקיפה"),
      H2("5.1 אימות שירות-לשירות (בין containers לינוקס לשרתי Windows/containers אחרים)"),
      P("ברירת מחדל מומלצת: mTLS (אישורי לקוח הדדיים). זו רמת ה-Zero Trust הבסיסית לתעבורת שירות-לשירות (לא משתמש-לשירות) לפי NIST SP 800-207, עובדת ברמת התעבורה (זול יותר מ-token introspection חוזר), ולא תלויה בכך שכל הצדדים יושבים תחת אותו ספק ענן/IAM — רלוונטי במיוחד כשחלק מהקוראים הם שרתי Windows חיצוניים."),
      CompareTable(
        ["חלופה", "מתי מתאימה"],
        [
          ["mTLS עם אישורים (מומלץ)", "ברירת המחדל — קבוצת קוראים ידועה ויציבה יחסית (שרתי Windows, containers פנימיים)"],
          ["מפתחות API / סוד משותף", "רק כפתרון זמני כשהצוות שולט בשני הצדדים; לא מתאים כברירת מחדל כי מפתח שדלף תקף עד ביטול ידני"],
          ["OAuth2 client-credentials", "אם כבר יש ספק זהות ארגוני שמנפיק JWT, ונדרשת בקרת הרשאות עדינה (“שלח job” מול “בטל job”) לכל קורא בנפרד"],
          ["SPIFFE/SPIRE", "התקן המודרני להנפקה/רענון אוטומטי של אישורים בקנה מידה דינמי (Kubernetes-native) — כנראה overkill כרגע לצי יציב יחסית של containers/שרתי Windows; מספיק CA פנימי (step-ca / Vault PKI) עם אישורים לתקופה קצרה (30–90 יום) ותהליך רענון מתועד"],
        ],
        [3000, 6000]
      ),
      Spacer(160),
      H2("5.2 הקשחת Ghostscript ומסנני CUPS"),
      P("Ghostscript (ה-filter שמריץ CUPS בפועל, gstoraster/pdftopdf) הוא משטח תקיפה אמיתי — לא תיאורטי. יש לו היסטוריה תקנית של עקיפות sandbox (למשל CVE-2024-29510 — עקיפת -dSAFER, נוצל בפועל; CVE-2023-36664 — RCE קריטי מקובץ מעוצב). יש להצמיד גרסה מתוקנת (≥10.03.1 ומעלה) ולעקוב אחרי CVE חדשים — זה סוג באג חוזר, לא תיקון חד-פעמי."),
      Bullet("להריץ את ה-filter כמשתמש non-root ייעודי, ללא shell."),
      Bullet("מערכת קבצים root לקריאה בלבד; רק תיקיית scratch/spool זמנית לכתיבה."),
      Bullet("seccomp profile שמצמצם ל-syscalls שהפילטר באמת צריך."),
      Bullet("AppArmor/SELinux profile שמגביל גישה לתיקיות ה-spool בלבד."),
      Bullet("הסרת כל capability שלא הכרחי (לא CAP_NET_RAW, לא CAP_SYS_ADMIN)."),
      Bullet("הגבלת משאבים (CPU time, זיכרון, גודל קובץ, מספר file descriptors) פר-job — מונע מניעת שירות מקובץ ענק/פגום."),
      Bullet("איסור גישה לרשת (network=none) מתהליך ה-render עצמו — ל-Ghostscript אין סיבה לגיטימית לפנות לרשת."),
      Spacer(),
      H2("5.3 SSRF — אם נתמכת הפניה לכתובת (סעיף 3)"),
      Bullet("allowlist של מקורות מאושרים בלבד — לא blocklist."),
      Bullet("resolve של ה-DNS בעצמנו ובדיקת ה-IP שהתקבל מול רשימת כתובות אסורות (רשתות פנימיות 10.0.0.0/8 וכו׳, 127.0.0.0/8, ובמיוחד 169.254.169.254 — כתובת metadata של ענן) לפני חיבור."),
      Bullet("אימות חוזר על ה-IP שאליו מתבצע החיבור בפועל (לא רק בזמן ה-DNS lookup) — כדי לסגור פרצת DNS rebinding."),
      Spacer(),

      // ---------- 6. Crash recovery extension ----------
      H1("6. התאוששות מנפילות ב-HA (עדכון לסעיף 8 במסמך המקורי)"),
      P("שתי תוספות קריטיות שעולות דווקא בהקשר של ריבוי שרתים (לא היו רלוונטיות באותה חדות למופע בודד):"),
      Bullet("Leader election — לא נדרש כאן. הכלל: כשה-workers הם צרכנים חסרי-מצב (stateless) של תור משותף, ולכל job יש מפתח ייחודי (idempotency key) ומנגנון claim מוגבל בזמן (סעיף 2), אין צורך ברכיב leader-election/Raft נפרד — התור/broker כבר מספק את ה-consensus הזה מבפנים. יוצא דופן יחיד: אם יש משימה תקופתית שחייבת לרוץ פעם אחת בלבד (למשל ניקוי/הצלבת מצב מול CUPS) — פותרים עם נעילה (advisory lock ב-DB) שכל node יכול לנסות לתפוס, לא עם שכבת leader-election נפרדת."),
      Bullet("סיכון ההדפסה הכפולה הפיזית: זו הנקודה הכי עדינה ב-HA. אם node קרס אחרי שהעמוד כבר יצא בפועל מהמדפסת אך לפני שהספיק לסמן את ה-job כ“הושלם”, ה-reaper יחזיר את ה-job ל-worker אחר — וזה עלול להדפיס שוב על נייר אמיתי. בניגוד לרישום כפול ב-DB (זול לתקן), הדפסה כפולה על נייר היא בזבוז בלתי הפיך. לכן: לפני כל ניסיון חוזר על job שהיה “בטיפול” בזמן קריסה, יש לבדוק בפועל את סטטוס ה-job מול cupsd/המדפסת המקוריים אם אפשר (בדיוק כמו כלי הבדיקה שכבר קיים מה-POC) — ורק אם אי אפשר לקבוע בוודאות, להעדיף לא להדפיס שוב ולסמן לבדיקה ידנית, על פני הדפסה כפולה “ליתר ביטחון”. זה שונה מהעיקרון הכללי בסעיף 8 במסמך המקורי (“עדיף בטוח יבוצע מאשר בדיוק פעם אחת”) — כאן ההשלכה היא פיזית ובלתי הפיכה, לא רישום כפול, ולכן שוקלים לכיוון ההפוך."),
      Bullet("כיבוי מסודר (rolling update/restart): טיפול ב-SIGTERM שמוריד מיידית את בדיקת ה-readiness (מפסיק לקבל jobs חדשים), ממתין לסיום ה-jobs שכבר בטיפול עד גבול זמן סביר, ורק אז יוצא. במידה ורץ תחת Kubernetes — preStop hook קצר לפני ה-SIGTERM כדי לתת ל-load balancer זמן להפסיק לנתב אליו, ו-terminationGracePeriodSeconds מותאם לזמן ה-job הארוך ביותר הצפוי."),
      Spacer(),

      // ---------- 7. Capacity ----------
      H1("7. תכנון קיבולת ותקציב משאבים (הערכה ראשונית)"),
      P("מבוסס על נתוני העומס שכבר נמדדו בפועל ב-POC (700 jobs, ~62ms זמן קבלה ממוצע, Ghostscript ~0.1–0.6 CPU-sec ו-~20–150MB זיכרון פר-render) ועל נתוני טביעת הרגל של NATS מהחקירה. אלו הערכות התחלתיות לתכנון — יש לאמת בהעמסה אמיתית על החומרה בפועל, לא מספרי סלע:"),
      CompareTable(
        ["רכיב", "הערכת תקציב פר-node"],
        [
          ["Gateway/Worker (תהליך עצמו, ללא CUPS)", "0.5–1 vCPU, 256–512MB — עומס קליל, רוב העבודה היא I/O מול התור/DB"],
          ["CUPS + ippfix + Ghostscript (render)", "1–2 vCPU, 512MB–1GB, תלוי במקביליות renders בו-זמנית פר-node"],
          ["NATS JetStream node (אשכול של 3)", "בסביבות 0.5 vCPU, מתחת ל-300MB — בינארי בודד ללא JVM; ראה סעיף 2"],
        ],
        [4000, 5000]
      ),
      Spacer(),

      // ---------- 8. Gaps I added ----------
      H1("8. פערים שזוהו ונוספו ביוזמתי"),
      Callout(
        "לתשומת לבך",
        "השלמות הבאות לא נשאלו במפורש בדרישות, אך בלעדיהן הפתרון לא באמת שלם — ולכן נוספו:",
        "FBE5D6", "C55A11"
      ),
      Spacer(80),
      Bullet("מסד הנתונים עצמו (מחזיק job store + audit) הוא נקודת כשל יחידה — אם הוא לא רץ ב-HA (למשל Postgres עם replication/cluster), כל הרעיון של N+1 בשכבת ה-Gateway קורס ברגע שהמסד נופל. זו לא הרחבה קוסמטית — בלי זה, ה-HA שביקשת קיים רק על הנייר."),
      Bullet("מנגנון הפצת “קטלוג המדפסות” (סעיף 11 במסמך המקורי) לכל ה-nodes באופן זהה ומעודכן — נדרש תהליך reconciliation (מקור אמת מרכזי + job שרץ בכל node, בעלייה ובאופן תקופתי, ומיישם lpadmin בהתאם) כדי שההנחה “כל node יכול לשרת כל מדפסת” תהיה נכונה בפועל, לא רק בהנחת יסוד."),
      Bullet("הגבלת קצב (rate limiting) פר-קורא/שירות — כדי ששרת Windows אחד “רועש” לא יחניק קוראים אחרים על אותו תור משותף."),
      Bullet("בדיקות תקינות (health checks) פר-מדפסת בפועל, לא רק ברמת השירות הכללי — היה סעיף פתוח במסמך המקורי (13), עכשיו יש לו מנגנון קונקרטי: ניצול סטטוס cups-browsed + polling תקופתי מול כל מדפסת."),
      Bullet("סנכרון שעונים (NTP) בין כל ה-nodes — קריטי לכך שסדר האירועים בלוג ובאודיט יהיה אמין כשמשווים אירועים בין containers שונים; בלי זה, שרשרת ה-hash ב-audit (סעיף 4.2) עלולה להיראות לא עקבית גם כשהיא תקינה."),
      Spacer(),

      // ---------- 9. Failure modes ----------
      H1("9. מיפוי כשלים אפשריים ותוכנית טיפול"),
      CompareTable(
        ["כשל אפשרי", "השפעה", "טיפול מוצע"],
        [
          ["node קורס אחרי שהעמוד כבר יצא מהמדפסת, לפני שסומן כהושלם", "הדפסה כפולה בפועל (בזבוז נייר בלתי הפיך)", "בדיקת סטטוס מול cupsd/המדפסת לפני retry; אם לא ניתן לוודא — לא להדפיס שוב, לסמן לבדיקה ידנית (ר׳ סעיף 6)"],
          ["מסד ה-Job store/Audit נופל", "כל המערכת נעצרת — תלות קריטית יחידה", "הרצה ב-replication/cluster (ר׳ סעיף 8); ניטור ייעודי + התרעה מיידית; ה-Gateway צריך להחזיר שגיאה ברורה ולא “לבלוע” בשקט"],
          ["כל 3 nodes של התור נופלים בו-זמנית", "הפסקת קליטת בקשות חדשות", "R3 replication + ניטור quorum; התרעה כבר כשnode אחד נופל (עדיין פעיל, אך בסיכון)"],
          ["קורא בודד (שרת Windows/container) שולח בקצב גבוה מדי", "הרעבת קוראים אחרים על אותו תור משותף", "rate limiting פר-caller (ר׳ סעיף 8)"],
          ["קובץ הדפסה זדוני מנצל חולשה ב-Ghostscript", "הרצת קוד על ה-node המארח", "הקשחת containers מסעיף 5.2 + עדכוני גרסה שוטפים ל-Ghostscript"],
          ["הפניה לכתובת (סעיף 3) מנוצלת ל-SSRF", "חשיפת משאבים/רשתות פנימיות", "allowlist + resolve-then-validate מסעיף 5.3"],
          ["worker קורס בלי שה-reaper תופס את זה בזמן סביר", "job “אבוד” בפועל (נשאר תקוע ב“בטיפול”)", "ניטור זמן שהייה מקסימלי בסטטוס “בטיפול”, עם התרעה נפרדת מה-DLQ הרגיל"],
          ["שעונים לא מסונכרנים בין nodes", "סדר אירועים לא אמין בלוג/audit, קושי בתחקור", "NTP חובה על כל ה-nodes (ר׳ סעיף 8)"],
        ],
        [2600, 3200, 3200]
      ),
      Spacer(),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("print-server-spec-ha.docx", buf);
  console.log("written");
});
