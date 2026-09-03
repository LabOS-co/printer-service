const fs = require("fs");
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
  TableOfContents, PageBreak, convertInchesToTwip, VerticalAlign,
  Numbering, LevelFormat,
} = require("docx");

const FONT = "Arial";
const RTL = true;

function P(text, opts = {}) {
  const { bold, size, color, italic, alignment = AlignmentType.RIGHT, spacingAfter = 120, heading, spacingBefore } = opts;
  return new Paragraph({
    heading,
    bidirectional: RTL,
    alignment,
    spacing: { after: spacingAfter, before: spacingBefore || 0 },
    children: [
      new TextRun({ text, bold, italics: italic, size, color, font: FONT }),
    ],
  });
}

function Bullet(text, opts = {}) {
  return new Paragraph({
    bidirectional: RTL,
    alignment: AlignmentType.RIGHT,
    numbering: { reference: "bullet-list", level: 0 },
    spacing: { after: 80 },
    children: [new TextRun({ text, font: FONT, bold: opts.bold, size: opts.size })],
  });
}

function H1(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_1,
    bidirectional: RTL,
    alignment: AlignmentType.RIGHT,
    spacing: { before: 360, after: 160 },
    children: [new TextRun({ text, font: FONT, bold: true, color: "1F4E78" })],
  });
}

function H2(text) {
  return new Paragraph({
    heading: HeadingLevel.HEADING_2,
    bidirectional: RTL,
    alignment: AlignmentType.RIGHT,
    spacing: { before: 240, after: 120 },
    children: [new TextRun({ text, font: FONT, bold: true, color: "2E74B5" })],
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
                children: [new TextRun({ text: label, bold: true, font: FONT, color: borderColor, size: 22 })],
              }),
              new Paragraph({
                bidirectional: RTL,
                alignment: AlignmentType.RIGHT,
                children: [new TextRun({ text, font: FONT, size: 21 })],
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
        bidirectional: RTL, alignment: AlignmentType.RIGHT,
        spacing: { after: 40 },
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
        children: [new TextRun({ text: "איפיון שרת הדפסה מרכזי", bold: true, size: 56, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "Print Gateway / Worker Service", size: 30, font: FONT, color: "2E74B5" })] }),
      new Paragraph({ spacing: { after: 80 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "LAB-16894", size: 24, font: FONT })] }),
      new Paragraph({ spacing: { after: 600 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "מבוסס על ה-POC שאומת פיזית: CUPS + IPP + ippfix", size: 22, italics: true, font: FONT, color: "555555" })] }),
      Callout("סטטוס המסמך", "טיוטה ראשונה לדיון — 2026-07-20. מסמך איפיון בלבד, ללא קוד. כל מקום שבו יש יותר מדרך סבירה אחת מסומן בבירור כ“אפשרות 1 / אפשרות 2”, בלי הכרעה — לדיון משותף.", "FFF2CC", "BF8F00"),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- TOC ----------
      H1("תוכן עניינים"),
      new TableOfContents("תוכן עניינים", { hyperlink: true, headingStyleRange: "1-2" }),
      new Paragraph({ children: [new PageBreak()] }),

      // ---------- 1. Background ----------
      H1("1. רקע — מה כבר הוכח ב-POC"),
      P("ב-POC (בתקייה C:\\printerSearch) הוכח שאפשר להדפיס PDF למדפסת רשת אמיתית בלי Windows Print Spooler, כולל פתרון לבאג IPP לא-תקני במדפסת. הודפס בפועל, פעמיים, על נייר אמיתי."),
      P("שלושה רכיבים מרכיבים את הפתרון שהוכח:"),
      Bullet("CUPS (בקונטיינר לינוקס) — מבצע את ההדפסה בפועל: מקבל IPP, הופך PDF ל-raster, שולח למדפסת."),
      Bullet("ippfix — פרוקסי קטן שמתקן שדה IPP שבור שהמדפסת מחזירה, כדי ש-CUPS יוכל בכלל לדבר איתה."),
      Bullet("PPD סטטי — “כרטיס יכולות” של המדפסת שנוצר פעם אחת מראש, במקום להיבנות מחדש בכל הדפסה."),
      P("המסמך הזה לא עוסק עוד ב“איך מדברים עם המדפסת” — זה כבר פתור. הוא עוסק בשכבה שמעליו: איך בקשות הדפסה מגיעות מבחוץ, איך המערכת מנהלת אותן, ומה קורה כשמשהו משתבש."),
      Spacer(),

      // ---------- 2. Architecture ----------
      H1("2. איך המערכת עובדת — זרימה כללית"),
      P("שרשרת הפעולות המוצעת, מהלקוח ועד הנייר:"),
      CodeBox([
        "1. לקוח   →   שולח בקשת הדפסה ל-Print Gateway",
        "2. Gateway →   שומר את הבקשה (Job) במסד נתונים, מחזיר אישור מיידי ללקוח",
        "3. Worker  →   שולף Job שממתין, מביא את הקובץ להדפסה",
        "4. Worker  →   שולח את הקובץ ל-CUPS (דרך ippfix) ב-IPP",
        "5. CUPS    →   מדפיס בפועל למדפסת הפיזית",
        "6. Gateway →   מעדכן ללקוח (polling / callback) מה קרה בסוף",
      ]),
      Spacer(80),
      P("שני התיבות “Gateway” ו-“Worker” יכולות להיות באותו תהליך בהתחלה (מונוליט), ולהתפצל בהמשך — זה לא משנה שום החלטה אחרת במסמך.", { italic: true, size: 19 }),
      Spacer(),

      // ---------- 3. Decision: intake ----------
      H1("3. החלטה פתוחה: איך מקבלים בקשת הדפסה"),
      H2("אפשרות 1 — בקשה ישירה (סינכרונית)"),
      P("הלקוח שולח בקשה, השרת מבצע את ההדפסה מיד, ורק בסוף מחזיר תשובה אחת — “הצליח” או “נכשל”."),
      H2("אפשרות 2 — כניסה לתור (אסינכרונית)"),
      P("הלקוח שולח בקשה, מקבל אישור מיידי (“התקבל, מספר X”), וממשיך הלאה. worker נפרד מבצע את ההדפסה ברקע. הלקוח בודק מאוחר יותר מה קרה."),
      CompareTable(
        ["", "אפשרות 1: בקשה ישירה", "אפשרות 2: תור"],
        [
          ["יתרונות", "פשוט להטמעה; אין רכיב תור נוסף", "הלקוח לא נחסם; סופג עומסים; קל להוסיף ניסיון חוזר אוטומטי"],
          ["חסרונות", "הלקוח מחכה זמן לא ידוע; תקלת רשת/מדפסת תוקעת גם את הבקשה", "מורכבות נוספת: צריך מקום לשמור סטטוס ומנגנון בדיקה"],
          ["מתי מתאים", "נפח נמוך מאוד, לקוח בודד שחייב לדעת מיד", "כל מקרה שבו יש יותר מלקוח אחד או יותר ממדפסת אחת"],
        ],
        [1800, 3600, 3600]
      ),
      Spacer(160),
      Callout(
        "המלצה",
        "אפשרות 2 (תור). זו לא המצאה שלנו — זו הדרך שגם Microsoft (Universal Print) וגם PaperCut בחרו: השרת המרכזי הוא “מתאם” שמקבל את הבקשה ומיד עונה, וההדפסה בפועל קורית ברקע ליד המדפסת (בדיוק כמו CUPS+ippfix אצלנו). זו גם המגמה הכללית בכל מערכת שמדברת עם משאב חיצוני לא-אמין (מדפסת, פקס, שירות חיצוני) — לא רק בהדפסה. חשוב: כלפי הלקוח זה עדיין יכול להיראות כמו “REST רגיל” (POST להגשה + GET לבדיקת סטטוס) — ה“תור” הוא פנימי, לא חייב להיות נראה ללקוח.",
      ),
      Spacer(),

      // ---------- 4. Decision: file delivery ----------
      H1("4. החלטה פתוחה: איך מקבלים את קובץ ההדפסה"),
      H2("אפשרות 1 — שליחה ישירה (הלקוח מצרף את הקובץ)"),
      P("הלקוח שולח את קובץ ה-PDF עצמו בתוך הבקשה."),
      H2("אפשרות 2 — הפניה לכתובת (הלקוח שולח רק קישור)"),
      P("הלקוח שולח רק כתובת (URL/נתיב) שבה הקובץ כבר נמצא, והשרת הולך להביא אותו בעצמו כשהוא מוכן לעבד."),
      CompareTable(
        ["", "אפשרות 1: שליחה ישירה", "אפשרות 2: הפניה לכתובת"],
        [
          ["יתרונות", "פשוט ללקוח חד-פעמי; בקשה אחת, בלי תלות חיצונית", "קל משקל; מתאים לשירות פנימי שכבר שמר את הקובץ איפשהו"],
          ["חסרונות", "בקשות גדולות עלולות להיתקל במגבלות; צריך מקום זמני לשמור את הקובץ עד שמדפיסים", "תלות ברכיב אחסון חיצוני; שאלת אבטחה — לא לתת לשרת להוריד מכל כתובת שהלקוח יבחר"],
          ["מתי מתאים", "לקוחות אינטראקטיביים / חיצוניים", "שירותים פנימיים אוטומטיים שכבר מייצרים/שומרים את המסמך"],
        ],
        [1800, 3600, 3600]
      ),
      Spacer(160),
      Callout(
        "המלצה",
        "לתמוך בשתיהן — כי יש שני סוגי צרכנים שונים לגמרי. שירות פנימי שכבר מייצר PDF ושומר אותו איפשהו — עדיף שישלח קישור בלבד. לקוח אינטראקטיבי חד-פעמי — עדיף שישלח את הקובץ ישירות. חשוב לזכור: הקובץ חייב “לשרוד” עד שה-worker בפועל מדפיס אותו — לא למחוק אותו מייד אחרי קבלת הבקשה.",
      ),
      Spacer(),

      // ---------- 5. API fields ----------
      H1("5. מבנה בקשת הדפסה — טיוטה ראשונית"),
      P("שדות מוצעים לבקשת הדפסה (לא סופי):"),
      CompareTable(
        ["שדה", "חובה?", "הערה"],
        [
          ["מזהה מדפסת", "כן", "מזהה לוגי פנימי, לא כתובת IP — ר' סעיף 11"],
          ["הקובץ עצמו / קישור לקובץ", "אחד מהשניים", "ר' סעיף 4"],
          ["גודל דף, רזולוציה, צבע, טווח עמודים, מספר עותקים", "לא", "ברירת מחדל לפי המדפסת"],
          ["רמת עדיפות", "לא", "ר' סעיף 10"],
          ["כתובת לעדכון (callback)", "לא", "התראה כשההדפסה הסתיימה, במקום שהלקוח יבדוק בעצמו"],
          ["מפתח ייחודי לבקשה (idempotency key)", "מומלץ מאוד", "מונע הדפסה כפולה אם הלקוח שולח שוב את אותה בקשה — ר' סעיף 8"],
          ["מזהה מבקש / מזהה מעקב", "כן", "לצורך לוגים ומעקב — ר' סעיף 6"],
        ],
        [2200, 1300, 5500]
      ),
      Spacer(),

      // ---------- 6. Logging ----------
      H1("6. לוגים"),
      P("הכלל המרכזי: לכל שורת לוג, בכל רכיב במערכת, חייב להיות אותו מספר מזהה של הבקשה (Job). כך אפשר לשחזר את “חיי” בקשה בודדת מקצה לקצה — מהרגע שהתקבלה ועד שהנייר יצא מהמדפסת."),
      P("מה חשוב לרשום בפועל:"),
      Bullet("קבלת בקשה: לאיזו מדפסת, גודל הקובץ, מי ביקש."),
      Bullet("כל מעבר סטטוס (התקבל → בטיפול → הודפס/נכשל) עם שעה מדויקת."),
      Bullet("הפנייה בפועל ל-CUPS/IPP והתשובה המדויקת שהתקבלה — לא רק “הצליח/נכשל”, אלא הקוד המדויק (ב-POC כבר ראינו שגיאות ספציפיות שדורשות אבחון שונה)."),
      Bullet("כל כשל, כולל מספר הניסיון (למשל “ניסיון 2 מתוך 3”)."),
      P("חשוב: לא לרשום תוכן מסמכים רגישים בלוג עצמו, רק מטא-דאטה (שם קובץ, גודל, מזהים). לקבוע כמה זמן שומרים לוגים (למשל 30-90 יום) ולנקות אוטומטית."),
      Spacer(),

      // ---------- 7. Failure handling ----------
      H1("7. מה עושים כשנכשלנו"),
      P("הצעד הראשון הוא לסווג את סוג הכשל — כי הטיפול הנכון שונה לגמרי:"),
      CompareTable(
        ["סוג כשל", "דוגמה", "מה עושים"],
        [
          ["זמני (אפשר לנסות שוב)", "מדפסת עסוקה, תקלת רשת רגעית", "לנסות שוב אוטומטית, עם המתנה שהולכת וגדלה בין ניסיון לניסיון"],
          ["קבוע (אין טעם לנסות שוב)", "מדפסת לא קיימת, קובץ פגום", "לסמן כנכשל מיד ולהתריע — לא לבזבז ניסיונות"],
          ["לא ברור", "החיבור נסגר אחרי שכבר נשלח להדפסה, לפני שהתקבל אישור", "לבדוק בפועל מול המדפסת/CUPS מה קרה בפועל, לפני שמנסים שוב — כדי לא להדפיס פעמיים"],
        ],
        [2200, 3300, 3500]
      ),
      Spacer(160),
      Callout(
        "המלצה",
        "מספר ניסיונות מוגבל (למשל 3-5), לא בלי סוף. אחרי שמיצינו את הניסיונות — הבקשה לא נעלמת, היא עוברת ל“רשימת כשלים” נפרדת עם כל הפרטים (מה נשלח, מה הייתה השגיאה, כמה ניסיונות בוצעו), כדי שמישהו יוכל לבדוק ידנית. חשוב: הרשימה הזו צריכה להיות דבר שמישהו בפועל בודק ומקבל עליו התראה — לא ערימת לוגים ששוכבת בלי שאף אחד מסתכל עליה.",
      ),
      Spacer(),

      // ---------- 8. Crash recovery ----------
      H1("8. התאוששות מקריסות"),
      P("זו כנראה הנקודה הכי קריטית לשירות מרכזי: אם השירות קורס, אסור לאבד הדפסה, ואסור גם להדפיס אותה פעמיים."),
      Bullet("כל בקשה נשמרת קודם במסד נתונים, ורק אחר כך מוחזר אישור ללקוח — כך שגם אם השירות קורס מייד אחרי, הבקשה כבר נשמרה ולא אבדה."),
      Bullet("העיקרון המקובל בתעשייה: עדיף לוודא “בטוח יבוצע (אולי פעמיים)” מאשר לנסות להבטיח “בדיוק פעם אחת” — זה קשה בהרבה טכנית. הפתרון לבעיית “פעמיים”: מפתח ייחודי לכל בקשה (idempotency key) — לפני כל ניסיון חוזר בודקים אם כבר נשלחה הדפסה מוצלחת עם אותו מפתח, ואם כן לא שולחים שוב."),
      Bullet("אם worker קורס באמצע עבודה על בקשה מסוימת, צריך מנגנון “שעון” שמזהה בקשה שנתקעה בלי עדכון יותר מדי זמן, ומחזיר אותה לתור לניסיון נוסף."),
      Bullet("כשכל השירות עולה מחדש אחרי קריסה, הוא צריך לבדוק את כל הבקשות שהיו “בטיפול” ברגע הקריסה, ולברר בפועל מול CUPS/המדפסת מה קרה להן בפועל — בדיוק כמו שכלי הבדיקה שכבר קיים ב-POC כבר יודע לעשות."),
      Bullet("שווה לנצל את זה ש-CUPS עצמו כבר שומר תור פנימי — ברגע שההדפסה הגיעה בהצלחה ל-CUPS, היא ממשיכה גם אם השרת שלנו קורס אחרי זה."),
      Spacer(),

      // ---------- 9. Security ----------
      H1("9. אבטחה"),
      Bullet("צריך לבדוק מי בכלל מורשה לשלוח בקשת הדפסה, ולאיזו מדפסת."),
      Bullet("אם תומכים באפשרות “קישור לקובץ” (סעיף 4) — אסור לתת לשרת להוריד מכל כתובת שהלקוח בוחר; יש להגביל לרשימת מקורות מוכרים ובטוחים, אחרת לקוח עוין יכול לגרום לשרת לפנות לכתובות פנימיות רגישות."),
      Bullet("לא לשמור תוכן מסמכים רגישים בלוגים (ר' סעיף 6)."),
      Bullet("לנקות קבצים זמניים אחרי שההדפסה הסתיימה — גם מטעמי מקום וגם מטעמי פרטיות."),
      Spacer(),

      // ---------- 10. Priorities/backpressure ----------
      H1("10. עדיפויות ועומסים"),
      Bullet("אם יש הרבה בקשות ומדפסת אחת בלבד, שווה לאפשר סימון “דחוף” מול “רגיל”."),
      Bullet("אם מדפסת מסוימת תקועה או לא זמינה, זה לא אמור לעצור בקשות למדפסות אחרות — כל מדפסת צריכה “תור משלה”, לא תור גלובלי אחד."),
      Bullet("לא לשלוח הרבה בקשות בבת אחת לאותה מדפסת אם היא לא תומכת בזה."),
      Spacer(),

      // ---------- 11. Printer catalog ----------
      H1("11. קטלוג מדפסות"),
      P("היום ב-POC יש טיפול ידני במדפסת אחת. שירות production צריך רשימה מסודרת של מדפסות: מזהה לוגי, כתובת, ה“כרטיס יכולות” (PPD) שלה, האם היא צריכה את תיקון ה-ippfix, ומה המדפסת תומכת בו (רזולוציה, גודל דף וכו')."),
      P("צריך גם תהליך קבוע (לא חד-פעמי) לצירוף מדפסת חדשה: לבדוק את היכולות שלה, לזהות אם יש לה תקלות דומות לזו שכבר נמצאה, וליצור עבורה “כרטיס יכולות” סטטי — באותה שיטה שכבר עבדה על המדפסת הראשונה."),
      Spacer(),

      // ---------- 12. Industry trend ----------
      H1("12. המגמה המובילה בעולם — ומה זה אומר עבורנו"),
      P("שתי דוגמאות מובילות בתחום ניהול הדפסה בענן, ומה שרלוונטי מהן:"),
      Bullet("Microsoft Universal Print — הענן משמש רק כמתאם/מנהל תצורה, לא כמבצע ההדפסה בפועל. עבור מדפסות ישנות שלא תומכות ב-IPP ישירות, מותקן רכיב מקומי (“Connector”) שמגשר בין הענן למדפסת — בדיוק אותו תפקיד ש-CUPS+ippfix ממלאים אצלנו."),
      Bullet("PaperCut — אותו עיקרון: השרת המרכזי מתאם בין רכיבים, אבל מסירת המסמך בפועל למדפסת נשארת מקומית, קרוב לפרינטר, לא עוברת דרך שרת מרוחק אחד."),
      Bullet("מעבר לתחום ההדפסה הספציפי, זו גם התבנית הסטנדרטית בכל מערכת שמתמודדת עם משאב איטי/לא-אמין (כרטיס אשראי, שירות חיצוני, מדפסת): לקבל את הבקשה מיד, לעבד ברקע, לוודא שהעיבוד עצמו לא יוצר כפילות אם הוא רץ פעמיים בטעות, ולנסות שוב אוטומטית עם המתנה שהולכת וגדלה — ולא לנסות שוב בלי סוף."),
      Spacer(120),
      Callout(
        "מסקנה מעשית",
        "הארכיטקטורה שהוצעה בסעיפים 2-4 (בקשה כלפי חוץ + תור פנימי + worker שמדבר עם CUPS+ippfix קרוב למדפסת) היא בדיוק הכיוון שגם Microsoft וגם PaperCut בחרו. זה לא ניחוש — זו הדרך שכבר הוכיחה את עצמה בקנה מידה גדול, ומומלץ להיצמד אליה גם כאן.",
        "E2EFDA", "548235"
      ),
      Spacer(),

      // ---------- 13. Open items ----------
      H1("13. נושאים פתוחים נוספים לדיון"),
      Bullet("בדיקות תקינות (health checks) — נקודת קצה שמראה אם השירות בריא, וזיהוי יזום של מדפסת שירדה (לא רק כשמגיעה אליה בקשה)."),
      Bullet("כיבוי מסודר — כשהשירות מקבל הוראת עצירה (לדוגמה בעדכון גרסה), לסיים קודם בקשות שכבר בטיפול ולא לנטוש אותן באמצע."),
      Bullet("איך בודקים את זה בבדיקות אוטומטיות בלי להדפיס בפועל בכל הרצה — צריך “מדפסת מדומה” לצורך זה."),
      Bullet("האם נדרש תיעוד “מי הדפיס מה ומתי” לצרכי בקרה/עלויות, מעבר ללוג התפעולי הרגיל."),
      Bullet("כמה worker-ים להריץ במקביל, והאם להגביל כמות בקשות בו-זמנית לכל מדפסת."),
      Spacer(),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("print-server-spec.docx", buf);
  console.log("written");
});
