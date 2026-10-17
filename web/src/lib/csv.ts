// Reading the header of a CSV file in the browser, so people can map its
// columns before uploading it. The server does the real parsing.

/**
 * Turns a column name into the name the server matches on: "Employee ID"
 * becomes "employee_id". Mirrors csvfile.Normalize in the core.
 */
export function normalizeColumn(name: string) {
  return (name.toLowerCase().match(/[\p{L}\p{Nd}]+/gu) ?? []).join("_");
}

/** The cells of the first record of a CSV text, quotes and all. */
export function parseHeader(text: string): string[] {
  text = text.replace(/^\uFEFF/, "");
  const cells: string[] = [];
  let cell = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (c === '"') {
        quoted = false;
      } else {
        cell += c;
      }
    } else if (c === '"' && cell.trim() === "") {
      quoted = true;
      cell = "";
    } else if (c === ",") {
      cells.push(cell.trim());
      cell = "";
    } else if (c === "\n" || c === "\r") {
      break;
    } else {
      cell += c;
    }
  }
  cells.push(cell.trim());
  return cells.filter((h) => h !== "");
}

/** The column names of a CSV file, read from its first 64 KiB. */
export async function readHeader(file: Blob) {
  return parseHeader(await file.slice(0, 64 << 10).text());
}

/** Drops the columns left empty in a mapping, so those fields keep their own names. */
export function cleanColumns(columns: Record<string, string>) {
  return Object.fromEntries(Object.entries(columns).filter(([, v]) => v.trim() !== ""));
}
