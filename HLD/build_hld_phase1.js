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
        children: [new TextRun({ text: "מסמך מסקנות — בסיס ל-HLD", bold: true, size: 52, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: Runs("שרת הדפסה מרכזי — שירות Gateway ו-Worker", { size: 26, color: "2E74B5" }) }),
      new Paragraph({ spacing: { after: 80 }, alignment: AlignmentType.CENTER,
        children: Runs("LAB-16894", { size: 24 }) }),
      Callout(
        "מטרת המסמך",
        "המסמך מציג את הכיוון המוצע לבניית שירות ההדפסה, ומשמש בסיס לדיון עם ההנהלה ומעצבי המערכת. בנושאים שיש להם כרגע כיוון ברור, מבוסס על מה שכבר נבדק בפועל — מוצג הכיוון הזה ישירות. בנושאים שיש בהם כמה חלופות אמיתיות ועדיין לא הוכרעו, המסומנים כ“עדיין פתוח”, מוצגות האפשרויות עם המלצה — לדיון והכרעה משותפים. שום דבר במסמך הזה אינו סופי — גם מה שמוצג כאן כהמלצה ברורה ניתן לשינוי על ידי ההנהלה. שלב זה (Phase 1) מתמקד בפריסה על מכונה אחת בלבד; הרחבה לכמה מכונות מתוארת בקצרה כ-Phase 2 בסוף המסמך.",
        "FFF2CC", "BF8F00"
      ),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- TOC ----------
      H1("תוכן עניינים"),
      new TableOfContents("תוכן עניינים", { hyperlink: true, headingStyleRange: "1-2" }),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- 1. Architecture ----------
      H1("1. ארכיטקטורה כללית"),
      P("הזרימה הכללית, מרגע שליחת הבקשה ועד שהנייר יוצא:"),
      Bullet("הלקוח (המערכת שלנו ששולחת בקשות הדפסה) שולח בקשת הדפסה ל-REST API של ה-Gateway."),
      Bullet("ה-Gateway מטפל בבקשה. מתי בדיוק חוזרת התשובה ללקוח — מיד, או רק אחרי שההדפסה הסתיימה בפועל — זו עדיין החלטה פתוחה, ר׳ סעיף 4.1."),
      Bullet("השירות שמטפל בבקשה מעביר את הקובץ ל-CUPS (דרך ippfix), באמצעות IPP."),
      Bullet("CUPS שולח את ההדפסה למדפסת הפיזית."),
      Bullet("הלקוח מקבל או בודק את הסטטוס הסופי — האופן המדויק גם הוא עדיין פתוח, ר׳ סעיף 5."),
      Spacer(80),
      P("מבנה הפריסה (Phase 1): מכונת לינוקס אחת, שעליה רצים:", { bold: true }),
      Bullet("שני שירותים זהים (Gateway/Worker) — לפחות 2, כך שאם אחד נופל או יוצא לתחזוקה, השני ממשיך לעבוד."),
      Bullet("CUPS אחד, משותף לשני השירותים."),
      P("זה מגן מפני נפילה של תהליך/שירות בודד. זה לא מגן מפני נפילה של המכונה עצמה — לכך צריך יותר ממכונה אחת (Phase 2)."),
      Spacer(),

      // ---------- 2. Print engine ----------
      H1("2. מנוע ההדפסה — CUPS + IPP + ippfix"),
      P("הכיוון הנוכחי, מבוסס על בדיקות שכבר בוצעו בפועל: ההדפסה מתבצעת באמצעות CUPS על לינוקס, מדבר IPP ישירות עם המדפסות, עם שני רכיבי עזר:"),
      H2("ippfix"),
      P("רכיב קטן שיושב בין CUPS למדפסת, ומתקן תשובות IPP לא תקינות שמדפסות מסוימות מחזירות. נמצאה בפועל מדפסת שהחזירה מידע שגוי (בניגוד לתקן), ו-ippfix פתר את זה בהצלחה."),
      H2("קובץ PPD קבוע"),
      P("PPD הוא “כרטיס יכולות” של המדפסת — גדלי נייר, רזולוציה, צבע וכו׳."),
      P("תמיד פונים למדפסת בפועל ומקבלים ממנה את הנתונים האמיתיים שלה. ברירת מחדל נכנסת לפעולה רק כשהתשובה שהתקבלה בפועל חסרה שדה מסוים או מכילה ערך שגוי — ואז מתקנים רק את השדה הבעייתי הזה מתוך תבנית ידועה, ומשאירים את כל שאר הנתונים כפי שהמדפסת דיווחה בפועל. את קובץ ה-PPD הסופי שומרים כדי לא לחזור על תהליך הבירור הזה בכל הדפסה בודדת."),
      H2("תוצאות בדיקות ביצועים"),
      P("בבדיקה שבוצעה על אותו קובץ:"),
      Bullet("CUPS קיבל עבודות הדפסה מהר פי כ-54 לעומת Windows Spooler עם SumatraPDF."),
      Bullet("Ghostscript צרך פי כ-5 פחות CPU."),
      Bullet("Ghostscript צרך בשיא פי כ-17 פחות זיכרון."),
      P("בהתבסס על הנתונים האלה, הפתרון המוצע לא כולל Windows Spooler או SumatraPDF."),
      Spacer(),

      // ---------- 3. HA ----------
      H1("3. זמינות גבוהה (Phase 1: מכונה אחת)"),
      Bullet("מומלץ להריץ לפחות 2 שירותים על אותה מכונה. מספר גבוה יותר אינו נדרש בשלב זה, לפי מה שנבדק עד כה."),
      Bullet("שני השירותים חולקים CUPS אחד. אין צורך במנגנון סנכרון בין כמה מופעי CUPS, כי יש רק מופע אחד."),
      Bullet("מכיוון שה-CUPS משותף, הוא רכיב קריטי יחיד: אם הוא קורס, שני השירותים מאבדים יכולת הדפסה בו-זמנית. מוצע להגדיר אותו כשירות שמופעל מחדש אוטומטית (systemd) אם הוא קורס."),
      Bullet("מסד הנתונים הייעודי להדפסה (ר׳ סעיף 4.2) צריך גיבוי סדיר כמו כל מסד נתונים production."),
      Spacer(80),
      P("מה זה כן פותר ומה זה לא פותר:", { bold: true }),
      CompareTable(
        ["מצב", "האם Phase 1 (מכונה אחת) פותר את זה?"],
        [
          ["שירות אחד קרס או יצא לעדכון", "כן — השני ממשיך"],
          ["CUPS עצמו קרס", "חלקית — אמור לעלות מחדש אוטומטית, אבל יש חלון זמן שבו אין הדפסה"],
          ["המכונה כולה נפלה (חומרה, מערכת הפעלה)", "לא — דורש יותר ממכונה אחת (Phase 2)"],
        ], [3500, 5500]
      ),
      Spacer(),

      // ---------- 4. Intake ----------
      H1("4. קליטת בקשות הדפסה — עדיין פתוח"),
      H2("4.1 בקשה ישירה מול תור"),
      CompareTable(
        ["", "אפשרות 1: בקשה ישירה (סינכרונית)", "אפשרות 2: תור (אסינכרוני)"],
        [
          ["יתרונות", "פשוטה יותר לפיתוח", "הלקוח לא נחסם; קל להתמודד עם עומס וניסיונות חוזרים"],
          ["חסרונות", "הלקוח מחכה זמן לא ידוע; תקלת מדפסת תוקעת את הבקשה", "דורש טיפול בסטטוסים ובבדיקת התקדמות"],
          ["מתי מתאים", "נפח נמוך מאוד", "יותר מבקשה אחת בו-זמנית"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout("המלצה", "תור אסינכרוני. כלפי הלקוח זה עדיין יכול להיראות כמו REST רגיל (שליחה + בדיקת סטטוס)."),
      Spacer(160),
      H2("4.2 אם בוחרים בתור — איזו תשתית תשמש אותו?"),
      Bullet("אפשרות A: להשתמש בתשתית ה-Jobs הקיימת, שהמערכת הקוראת (זו ששולחת את בקשות ההדפסה) כבר מריצה בעצמה, מבוססת טבלת DB קיימת."),
      Bullet("אפשרות B: לבנות מנגנון עצמאי וייעודי, רק לצורך הדפסה, נפרד מהמערכת הקוראת."),
      Spacer(80),
      Callout("המלצה", "אפשרות B — עצמאי, כדי לא להעמיס עוד עומס על מסד נתונים שכבר משרת מטרות אחרות. מכיוון שזה נוגע למערכת קיימת שלא רק שרת ההדפסה אחראי עליה, זו נקודה מרכזית לדיון, לא הכרעה סופית."),
      Spacer(),

      // ---------- 5. Status ----------
      H1("5. איך בודקים את סטטוס ההדפסה — עדיין פתוח"),
      Bullet("אפשרות 1 — תווך משותף: טבלת DB שגם המערכת הכללית וגם שרתי ההדפסה יכולים לגשת אליה."),
      Bullet("אפשרות 2 — בקשת סטטוס לפי trace_id: המערכת הכללית שולחת בקשה נקודתית “מה קרה לעבודה X”."),
      Bullet("אפשרות 3 — עדכון דרך התור המשותף (בהתאם למה שייבחר בסעיף 4.2): שינוי סטטוס משודר כהודעה בתור עצמו."),
      Bullet("אפשרות 4 — המתנה לתשובה: רלוונטי רק אם נבחרה “בקשה ישירה” בסעיף 4.1 — אז התשובה עצמה כוללת את הסטטוס הסופי."),
      P("הערה: אפשרויות 1 ו-2 הן בעצם שני צדדים של אותו מנגנון (טבלה משותפת + API שקורא ממנה). אפשרות 4 שייכת רק לנתיב הסינכרוני. ארבע האפשרויות פתוחות לדיון.", { italic: true, size: 19 }),
      Spacer(),

      // ---------- 6. File delivery ----------
      H1("6. מסירת קובץ ההדפסה — עדיין פתוח"),
      Bullet("אפשרות 1: הלקוח שולח את הקובץ ישירות (multipart)."),
      Bullet("אפשרות 2: הלקוח שולח רק כתובת לקובץ, והשרת מוריד אותו משם."),
      Spacer(80),
      Callout("המלצה", "לתמוך בשתיהן — צירוף ישיר כברירת מחדל עד כ-10MB, ומעליו או ללקוחות בנפח גבוה — הפניה לכתובת (אחסון תואם S3, כמו MinIO אם מדובר בפריסה מקומית). אם נתמכת הורדה מכתובת, נדרשת הגנת אבטחה (ר׳ סעיף 11) שהשרת לא יוריד מכתובות לא מאושרות."),
      Spacer(),

      // ---------- 7. Schema ----------
      H1("7. מבנה בקשת ההדפסה"),
      CompareTable(
        ["שדה", "חובה?", "הסבר"],
        [
          ["מזהה מדפסת", "כן", "מזהה לוגי מתוך קטלוג המדפסות (ר׳ סעיף 13), לא כתובת IP"],
          ["קובץ או הפניה לקובץ", "אחד מהם", "ר׳ סעיף 6"],
          ["גודל נייר, רזולוציה, צבע, טווח עמודים, עותקים", "לא", "ברירת מחדל לפי המדפסת"],
          ["עדיפות", "לא", "ר׳ סעיף 12"],
          ["Callback URL", "לא", "עדכון אוטומטי בסיום"],
          ["מפתח ייחודי (Idempotency Key)", "כן", "מונע הדפסה כפולה בניסיון חוזר — ר׳ סעיף 10"],
          ["מזהה שולח + trace_id", "כן", "לצורך מעקב בלוגים וב-Audit"],
        ], [2200, 1300, 5500]
      ),
      Spacer(),

      // ---------- 8. Logging + Audit ----------
      H1("8. לוגים ו-Audit"),
      P("מוצע להפריד בין שני יעדים:"),
      Bullet("לוג תפעולי (Kibana/ELK) — עבור מפתחים: איתור תקלות, דשבורדים, התראות."),
      Bullet("Audit במסד נתונים ייעודי — מי הדפיס מה, מתי, ומה הייתה התוצאה."),
      H2("מה יישמר ב-Audit"),
      CompareTable(
        ["שדה", "הסבר"],
        [
          ["actor, action, target", "מי ביצע, איזו פעולה, על איזו עבודה/מדפסת"],
          ["זמן ותוצאה", "UTC + קוד סיבה מדויק"],
          ["trace_id", "משותף ללוג התפעולי ולאודיט"],
          ["source_service, request_ip, sequence_number", "לצורך מעקב ואימות שהרשומות לא שונו"],
        ], [3000, 6000]
      ),
      Spacer(120),
      P("מוצע שרשומות ה-Audit יהיו ניתנות להוספה בלבד (לא ניתן למחוק/לערוך), מחוברות בשרשרת שמאפשרת לגלות שינוי, ונבדקות אוטומטית לפחות פעם ביום."),
      CompareTable(
        ["", "לוג תפעולי (Kibana)", "Audit (DB ייעודי)"],
        [
          ["שמירה", "30–90 יום", "תקופה ארוכה בהרבה, לפי דרישות ציות"],
          ["ניתן לשינוי", "כן", "לא"],
        ], [1800, 3600, 3600]
      ),
      Spacer(),

      // ---------- 9. Failure handling ----------
      H1("9. טיפול בכשלים וניסיונות חוזרים"),
      CompareTable(
        ["סוג כשל", "דוגמה", "טיפול"],
        [
          ["זמני", "מדפסת עסוקה, תקלת רשת רגעית", "ניסיון חוזר אוטומטי, עם זמן המתנה שהולך וגדל"],
          ["קבוע", "מדפסת לא קיימת, קובץ פגום", "כישלון מיידי + התראה"],
          ["לא ברור", "החיבור נסגר אחרי שליחה, לפני קבלת אישור", "בדיקה מול CUPS/המדפסת לפני ניסיון נוסף"],
        ], [2000, 3400, 3600]
      ),
      Spacer(120),
      Callout("המלצה", "בין 3 ל-5 ניסיונות. לאחר שכולם נכשלו — העבודה עוברת ל“תור כשלים” (DLQ) עם כל הפרטים, ומישהו מקבל עליה התראה ובודק אותה."),
      Spacer(),

      // ---------- 10. Crash recovery ----------
      H1("10. התאוששות מקריסות ומניעת הדפסה כפולה"),
      Bullet("לכל בקשה יש מפתח ייחודי (Idempotency Key). לפני ניסיון חוזר בודקים אם כבר בוצעה הדפסה מוצלחת עם אותו מפתח."),
      Bullet("עבודה “ננעלת” לזמן מוגבל בזמן טיפול. אם השירות שמטפל בה קרס, הנעילה פגה והעבודה חוזרת אוטומטית לתור."),
      Bullet("אין צורך במנגנון “מנהיג” קבוע בין השירותים — נעילה מוגבלת בזמן + מפתח ייחודי מספיקים."),
      H2("המקרה הכי רגיש: לא ברור אם ההדפסה יצאה"),
      P("ייתכן שהדף כבר יצא מהמדפסת, אבל השירות קרס לפני שסימן את העבודה כהצלחה."),
      Spacer(80),
      Callout("המלצה", "לפני ניסיון חוזר בודקים מול CUPS/המדפסת מה שאפשר לבדוק. אם עדיין אין ודאות — מדפיסים שוב. הטרייד-אוף: סיכון נמוך של הדפסה כפולה מדי פעם, לעומת סיכון של הדפסה “אבודה”."),
      Spacer(160),
      H2("כיבוי מסודר"),
      P("כשמקבלים הוראת עצירה: מפסיקים לקבל עבודות חדשות מיד, ממתינים שהעבודות שכבר בטיפול יסתיימו (עד גבול זמן סביר), ורק אז השירות נסגר."),
      Spacer(),

      // ---------- 11. Security ----------
      H1("11. אבטחה"),
      H2("11.1 זיהוי בין שירותים"),
      CompareTable(
        ["אפשרות", "מתי מתאימה"],
        [
          ["mTLS", "ברירת מחדל מוצעת — קבוצת קוראים יציבה יחסית"],
          ["מפתחות API", "פתרון זמני בלבד"],
          ["OAuth2 Client Credentials", "אם כבר קיים ספק זהויות ארגוני"],
        ], [3000, 6000]
      ),
      Spacer(120),
      H2("11.2 הקשחת Ghostscript והשירות"),
      Bullet("גרסת Ghostscript מעודכנת (יש באגי אבטחה מתועדים בגרסאות ישנות)."),
      Bullet("הרצה ללא הרשאות root, ללא גישה ל-Shell."),
      Bullet("מערכת קבצים לקריאה בלבד."),
      Bullet("הגבלת CPU וזיכרון לכל עבודת הדפסה."),
      Bullet("אין גישה לרשת מתהליך עיבוד הקובץ עצמו."),
      H2("11.3 הגנה מפני שימוש לרעה בכתובות (אם נתמכת אפשרות 2 מסעיף 6)"),
      Bullet("הורדה רק ממקורות מאושרים מראש."),
      Bullet("בדיקת כתובת ה-IP בפועל לפני ואחרי ההתחברות, כדי לחסום ניסיון לגשת לרשת הפנימית."),
      Spacer(),

      // ---------- 12. Priorities ----------
      H1("12. עדיפויות ועומסים"),
      Bullet("לכל מדפסת תור נפרד, כדי שמדפסת תקועה לא תעכב מדפסות אחרות. עבודה שממתינה למדפסת תקולה לא ננטשת — היא עדיין מטופלת לפי מדיניות הכשלים (סעיף 9). ה“תור הנפרד” רק מונע ממנה לעכב מדפסות אחרות שכן עובדות."),
      Bullet("לפחות שתי רמות עדיפות (דחוף/רגיל)."),
      Bullet("אין הגבלת קצב שדוחה או חוסמת קורא כלשהו. כל בקשה מתקבלת ונכנסת לתור, גם בעומס גבוה."),
      Bullet("יש ניטור והתראה אם קורא בודד שולח נפח חריג — לצורך מודעות בלבד, לא כדי לחסום אותו."),
      Bullet("אתר עם נפח הדפסות גבוה מטופל בהוספת שרתים באותו אתר, לא בהגבלת לקוחות."),
      Spacer(),

      // ---------- 13. Printer catalog ----------
      H1("13. קטלוג מדפסות והוספת מדפסת חדשה"),
      P("איפה זה נשמר: באותו מסד הנתונים הייעודי להדפסה (סעיף 4.2, אפשרות B), לא במסד של המערכת הקוראת."),
      P("מה זה כולל, לכל מדפסת: מזהה לוגי, כתובת, קובץ PPD ויכולות, האם היא זקוקה ל-ippfix, ואילו אפשרויות נתמכות."),
      P("מה זה חוסך בפועל:", { bold: true }),
      Bullet("לא צריך לנהל מו״מ חי מול המדפסת בכל הדפסה בודדת."),
      Bullet("אפשר להתייחס למדפסת לפי מזהה לוגי ולא כתובת IP קשיחה בקוד."),
      Bullet("מזין את מסך הסטטוס (סעיף 15)."),
      P("תהליך מוצע להוספת מדפסת חדשה: בדיקת יכולות, בדיקת בעיות IPP מוכרות, יצירת PPD קבוע, והפצת ההגדרה לשירותים."),
      Spacer(),

      // ---------- 14. Capacity ----------
      H1("14. הערכת משאבים (Phase 1: מכונה אחת)"),
      CompareTable(
        ["רכיב", "הערכה"],
        [
          ["שני שירותי Gateway/Worker (ללא CUPS)", "0.5–1 vCPU ו-256–512MB זיכרון, לכל שירות"],
          ["CUPS + ippfix + Ghostscript", "1–2 vCPU ו-512MB–1GB, תלוי בכמות הדפסות מקבילות"],
        ], [4000, 5000]
      ),
      Spacer(),

      // ---------- 15. Monitoring ----------
      H1("15. ניטור והצגת מצב הדפסה"),
      H2("מה CUPS מספק כברירת מחדל"),
      P("ל-CUPS יש ממשק Web מובנה (פורט 631), שמציג רשימת מדפסות, עבודות והיסטוריה, ומאפשר לעצור/לבטל עבודות."),
      P("מגבלות: זמין כברירת מחדל רק מתוך המכונה עצמה; הרשאות בסיסיות בלבד; מיועד לניהול ואבחון טכני, לא לחשיפה לבעלי עניין."),
      Spacer(80),
      Callout("המלצה", "ממשק CUPS יישאר זמין רק מקומית, לצורך דיבוג נקודתי על ידי מהנדס. הוא לא ייחשף כתצוגה לגורמים אחרים."),
      Spacer(160),
      H2("איך יוצג הסטטוס בפועל"),
      P("מוצע לבנות תצוגה/API פשוטים בתוך שירות ה-Gateway עצמו, שקוראים מהמסד הייעודי ומציגים תמונה ברורה: כמה בקשות בכל סטטוס לכל מדפסת, ומצב מקוון/לא-מקוון."),
      Spacer(80),
      H2("איך מוציאים את זה החוצה לדשבורד — עדיין פתוח"),
      CompareTable(
        ["", "Kibana Dashboard", "Prometheus + Grafana"],
        [
          ["יתרון", "לא דורש תשתית חדשה", "מתאים במיוחד למדדים מספריים והתראות"],
          ["חיסרון", "פחות מדויק למדדים מספריים", "דורש הרצת מערכת נוספת אם היא לא קיימת כבר"],
        ], [1800, 3600, 3600]
      ),
      Spacer(120),
      Callout("המלצה", "Kibana כברירת מחדל. אם בארגון כבר יש Prometheus/Grafana לשירותים אחרים — עדיף להשתמש בהם."),
      Spacer(),

      // ---------- 16. Failure map ----------
      H1("16. מפת תקלות אפשריות ופתרונות מוצעים"),
      CompareTable(
        ["תקלה אפשרית", "השפעה", "פתרון מוצע"],
        [
          ["שירות אחד קורס", "אין השפעה, השני ממשיך", "תקין מלכתחילה"],
          ["ה-CUPS המשותף קורס", "שני השירותים מאבדים יכולת הדפסה זמנית", "הפעלה מחדש אוטומטית + התראה"],
          ["שירות קורס אחרי שהעמוד יצא, לפני שסומן כהושלם", "הדפסה כפולה אפשרית", "בדיקה מול CUPS/המדפסת; אם אין ודאות — מדפיסים שוב"],
          ["מסד הנתונים הייעודי נופל", "המערכת נעצרת", "גיבויים סדירים, ניטור והתראה מיידית"],
          ["קורא בודד שולח נפח חריג", "אין חסימה", "ניטור והתראה בלבד"],
          ["קובץ זדוני מנצל חולשה ב-Ghostscript", "הרצת קוד על השרת", "הקשחה + עדכוני גרסה שוטפים"],
          ["הפניה לכתובת מנוצלת לגישה לרשת פנימית", "חשיפת מידע פנימי", "Allowlist ובדיקת IP"],
          ["עבודה נשארת תקועה ב“בטיפול” זמן רב", "עלולה ללכת לאיבוד", "ניטור זמן שהייה מקסימלי + התראה"],
        ], [2600, 3200, 3200]
      ),
      Spacer(),

      // ---------- Phase 2 ----------
      H1("Phase 2 (עתידי) — הרחבה לכמה מכונות"),
      P("כשאתר מסוים יזדקק ליותר ממכונה אחת בגלל נפח גבוה, יידרש מנגנון שמחבר בין המכונות ומשתף ביניהן מצב עבודות. לא מתוכנן בשלב זה — יידון כשהצורך יתעורר."),
      Spacer(),

      // ---------- Summary ----------
      H1("סיכום פשוט"),
      P("הכיוון המוצע: שירות הדפסה שרץ, בשלב זה, על מכונת לינוקס אחת — שני שירותים זהים לגיבוי, שחולקים CUPS משותף אחד. המערכת מטפלת בכשלים, שומרת Audit, ומנטרת עומסים בלי לחסום אף לקוח."),
      P("נושאים עדיין פתוחים לדיון:", { bold: true }),
      Bullet("בקשה ישירה מול תור (4.1)."),
      Bullet("אם תור — תשתית קיימת מול מנגנון עצמאי (4.2)."),
      Bullet("איך בודקים סטטוס הדפסה — 4 חלופות (5)."),
      Bullet("מסירת קובץ (6)."),
      Bullet("ניטור חיצוני: Kibana מול Prometheus/Grafana (15)."),
      Spacer(),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("print-gateway-hld-phase1.docx", buf);
  console.log("written");
});
