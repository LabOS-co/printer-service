const fs = require("fs");
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
  TableOfContents, PageBreak, VerticalAlign, LevelFormat,
} = require("docx");

const FONT = "Arial";
const RTL = true;

function P(text, opts = {}) {
  const { bold, size, color, italic, alignment = AlignmentType.RIGHT, spacingAfter = 120, spacingBefore } = opts;
  return new Paragraph({
    bidirectional: RTL,
    alignment,
    spacing: { after: spacingAfter, before: spacingBefore || 0 },
    children: [new TextRun({ text, bold, italics: italic, size, color, font: FONT })],
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
            bidirectional: RTL, alignment: AlignmentType.RIGHT, spacing: { after: 60 },
            children: [new TextRun({ text: label, bold: true, font: FONT, color: borderColor, size: 22 })],
          }),
          new Paragraph({
            bidirectional: RTL, alignment: AlignmentType.RIGHT,
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
        bidirectional: RTL, alignment: AlignmentType.RIGHT, spacing: { after: 40 },
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
      // ---------- Title ----------
      new Paragraph({ spacing: { before: 1200, after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "CUPS+IPP מול Windows Print Spooler", bold: true, size: 52, font: FONT, color: "1F4E78" })] }),
      new Paragraph({ spacing: { after: 200 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "השוואת ביצועים — זמן תגובה, תפוקה, CPU וזיכרון", size: 26, font: FONT, color: "2E74B5" })] }),
      new Paragraph({ spacing: { after: 600 }, alignment: AlignmentType.CENTER,
        children: [new TextRun({ text: "LAB-16894 — נספח לאיפיון שרת ההדפסה", size: 20, italics: true, font: FONT, color: "555555" })] }),
      Callout("שיטת הבדיקה", "צד CUPS: 700 הדפסות בס\"ה, מפוזרות על 15 תורי-CUPS/מדפסות-דמה שונות (10 תומכות PDF ישירות, 5 לא), concurrency=20. צד Windows: 60 הדפסות, concurrency=5, דרך SumatraPDF -print-to מול מדפסת מבוססת-קובץ עם דרייבר Brother אמיתי (לא PORTPROMPT, כדי לא להיתקע על דיאלוג) — נפח גדול יותר מהריצה הראשונית כדי לתת מדגם יציב יותר, בלי לבזבז נייר אמיתי ובלי לחכות שעות (SumatraPDF איטי בהרבה, ~3.3 שניות לעבודה בממוצע). אותו קובץ PDF (2 עמודים, A4) בשני הצדדים.", "FFF2CC", "BF8F00"),
      new Paragraph({ children: [new PageBreak()] }),

      H1("1. טבלת ההשוואה המרכזית"),
      P("צד CUPS: ממוצע על כל 700 ההדפסות (15 מדפסות). צד Windows: ממוצע על 60 הדפסות."),
      CompareTable(
        ["מדד", "CUPS + IPP (700 הדפסות, 15 מדפסות)", "Windows Spooler (60 הדפסות)", "מי עדיף"],
        [
          ["זמן תגובה ללקוח (per job)", "min 44.5ms / ממוצע 62.1ms / p95 94.8ms / max 127.5ms", "min 3.15s / ממוצע 3.36s / p95 3.52s / max 3.59s", "CUPS — פי ~54"],
          ["תפוקה (בקשות/שנייה)", "321.5 req/s", "1.5 req/s", "CUPS — פי ~214"],
          ["כשלים", "0 מתוך 700", "0 מתוך 60", "שווה"],
          ["עלות CPU של הרינדור (PDF→raster)", "~0.63 CPU-שנ׳/עבודה (Ghostscript, רק ב-5 מתוך 15 מדפסות)", "~3.09 CPU-שנ׳/עבודה (SumatraPDF, כל עבודה)", "CUPS/Ghostscript — פי ~5 יעיל יותר"],
          ["זיכרון שיא בו-זמני (~5 מופעים מקבילים)", "~153MB", "~2,650MB (2.65GB)", "CUPS/Ghostscript — פי ~17 פחות"],
          ["עלות ה\"מתאם\" עצמו (cupsd/spoolsv)", "~12.5MB, ~0.12 CPU-שנ׳ סה\"כ", "~47MB, ~0 CPU-שנ׳", "דומה, זניח בשני הצדדים"],
        ],
        [2400, 2700, 2400, 1500]
      ),
      Spacer(160),

      H1("2. פירוט מלא לכל אחת מ-15 המדפסות (700 ההדפסות)"),
      P("כל שורה = ~47 הדפסות (700 חולקו בין 15 המדפסות). כל 700 הצליחו (0 כשלים), אחרי שהועלתה מגבלת MaxJobs של CUPS (ר' סעיף 4)."),
      CompareTable(
        ["#", "יצרן / דגם", "PDF ישיר?", "כמות", "min", "ממוצע", "p95", "max"],
        [
          ["1", "HP LaserJet Pro M404", "כן", "47", "45.0ms", "62.1ms", "90.9ms", "111.4ms"],
          ["2", "Canon imageRUNNER ADVANCE", "כן", "47", "45.0ms", "61.8ms", "84.7ms", "108.5ms"],
          ["3", "Xerox VersaLink C405", "כן", "47", "44.8ms", "61.8ms", "88.4ms", "108.4ms"],
          ["4", "Epson WorkForce Pro", "כן", "47", "44.6ms", "61.9ms", "87.4ms", "106.7ms"],
          ["5", "Kyocera ECOSYS M2540", "כן", "47", "44.5ms", "61.4ms", "85.1ms", "106.6ms"],
          ["6", "Ricoh MP C3004", "כן", "47", "44.5ms", "62.7ms", "88.2ms", "119.1ms"],
          ["7", "Lexmark MX521", "כן", "47", "44.8ms", "61.9ms", "90.2ms", "116.0ms"],
          ["8", "Konica Minolta bizhub C3350", "כן", "47", "44.8ms", "62.2ms", "90.4ms", "115.9ms"],
          ["9", "Sharp MX-3070", "כן", "47", "45.1ms", "62.4ms", "92.7ms", "115.8ms"],
          ["10", "Dell Smart Printer S2815", "כן", "47", "44.9ms", "62.8ms", "94.8ms", "115.7ms"],
          ["11", "Brother HL-L2350DW", "לא", "46", "44.9ms", "62.0ms", "89.9ms", "127.5ms"],
          ["12", "Samsung ProXpress M4020", "לא", "46", "44.9ms", "62.2ms", "90.0ms", "114.8ms"],
          ["13", "Zebra ZT411", "לא", "46", "44.9ms", "61.8ms", "89.8ms", "113.2ms"],
          ["14", "Star TSP143", "לא", "46", "44.9ms", "62.4ms", "91.9ms", "112.6ms"],
          ["15", "OKI B432", "לא", "46", "45.1ms", "61.9ms", "87.8ms", "112.5ms"],
        ],
        [500, 2600, 1100, 900, 1000, 1100, 1000, 900]
      ),
      Spacer(120),
      Callout("ממצא מרכזי מהפירוק לפי מדפסת", "זמן התגובה זהה סטטיסטית בין ה-10 שתומכות PDF ישירות (ללא רינדור בצד CUPS) לבין ה-5 שלא (רינדור אמיתי דרך Ghostscript) — כי CUPS מחזיר תשובה ברגע שה-job נכנס לתור, לפני שהרינדור בכלל מתחיל. ההבדל האמיתי בין הקבוצות לא מופיע כאן, אלא בעומס הרקע: מדידה נפרדת של תהליכי Ghostscript בפועל הראתה בדיוק 5 הפעלות — תואם במדויק ל-5 המדפסות שלא תומכות PDF ישירות (שורות 11-15). 10 המדפסות התומכות ב-PDF (שורות 1-10) לא גרמו לשום עלות רינדור בצד השרת.", "DDEBF7", "2E74B5"),
      Spacer(),

      H1("3. למה הפער כל-כך גדול — זה לא \"מהיר יותר\", זה ארכיטקטורה שונה"),
      P("CUPS מחזיר תשובה ללקוח ברגע שה-job נכנס לתור — הרינדור בפועל (Ghostscript, כשצריך) קורה ברקע, אחרי שהלקוח כבר קיבל תשובה. זו בדיוק הגישה של תור אסינכרוני שנדונה באיפיון (סעיף 3)."),
      P("SumatraPDF -print-to, לעומת זאת, חוסם את הקורא עד שהרינדור המלא + המסירה לספולר מסתיימים — גישה סינכרונית. זו לא \"תקלה\" בצד Windows, אלא פשוט האופן שבו כלי כזה בנוי: הוא צריך להיות אפליקציית-GUI מלאה עם מנוע רינדור PDF משלה כדי בכלל להעביר את התוכן לדרייבר המדפסת."),
      Spacer(),

      H1("4. ממצא אגבי: מגבלת MaxJobs של CUPS"),
      Callout("ממצא אגבי", "בניסיון הראשון להריץ את 700 ההדפסות נתקלנו בשגיאה אמיתית: \"Too many active jobs\" — ל-CUPS יש מגבלה מובנית (MaxJobs, ברירת מחדל 500) על כמות עבודות פעילות בו-זמנית בכל המערכת. מכיוון שמדפסות-הדמה מדמות גם מהירות הדפסה מכנית ריאלית (עבודות נשארות \"פעילות\" זמן ארוך), ב-700 הדפסות זה נחצה. הועלתה המגבלה (MaxJobs 2000) כדי שהריצה תושלם נקי. זו נקודת תכנון אמיתית לקיבולת (capacity planning) של שרת production — לא באג בקוד.", "FCE4D6", "C55A11"),
      Spacer(),

      H1("5. מה לא נכלל בהשוואה, ולמה"),
      Bullet("\"זמן סיום מלא\" (עד שה-job מגיע ל-completed בפועל, לא רק התקבל) נבדק גם הוא, אבל לא הוכנס לטבלה: מדפסת-הדמה (ippeveprinter) מדמה גם מהירות הדפסה מכנית ריאלית (8-27 שניות לעבודה) — זה מסתיר לגמרי את הפרש הרינדור האמיתי (חלקיק שנייה) ולא אומר כלום על יעילות התוכנה."),
      Bullet("בצד Windows לא הייתה סימולציה מקבילה של מהירות מכנית (כתיבה לקובץ, לא למדפסת אמיתית) — כך שהשוואת \"זמן סיום מלא\" בין הצדדים לא הייתה הוגנת, ולכן הושארו רק המדדים שבאמת משקפים עלות תוכנה/ארכיטקטורה."),
      Bullet("שמות היצרנים/דגמים במדפסות-הדמה הם תוויות בלבד על אותה תוכנת-דמה (ippeveprinter) — הבדיקה בוחנת את התנהגות השרת שלנו מול תרחישי PDF/לא-PDF שונים, לא תאימות חומרה אמיתית של כל יצרן."),
      Spacer(),

      H1("6. מסקנה"),
      Callout("מסקנה מעשית", "עבור שרת הדפסה מרכזי (LAB-16894), נתיב CUPS+IPP עדיף משמעותית על נתיב Windows Spooler+SumatraPDF בארבעה ממדים בלתי-תלויים, על מדגם גדול (700 מול 60 הדפסות): זמן תגובה ללקוח (פי ~54), תפוקה (פי ~214), עלות CPU לרינדור (פי ~5), וזיכרון בו-זמני לרינדור (פי ~17). התמיכה/אי-תמיכה ב-PDF ישיר של המדפסת לא משפיעה על זמן התגובה ללקוח (CUPS מחזיר תשובה מיד בכל מקרה) — רק על עומס הרינדור ברקע. זה מחזק את הכיוון שכבר הומלץ באיפיון (מבוסס על Microsoft Universal Print / PaperCut) — שכבת ביצוע מבוססת-CUPS קרוב לכל מדפסת, עם שכבת בקרה נפרדת שמנצלת את יתרון התור.", "E2EFDA", "548235"),
    ],
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync("cups-vs-spooler-comparison.docx", buf);
  console.log("written");
});
