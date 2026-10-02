// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// CSV parsing for the file panel's table preview.
//
// Hand-rolled rather than pulling in a parser library: the repo's convention is
// a lean dependency list, and a preview needs the CSV grammar (RFC 4180) rather
// than the streaming, type-coercion and error-recovery machinery a full library
// carries. The grammar is small enough to read in one sitting.
//
// Deliberately lenient. This renders whatever the agent or the user dropped in
// the workspace, and real CSVs are frequently a bit wrong — a stray quote, a
// ragged row, mixed line endings. A preview that renders 99% of a slightly
// malformed file is far more useful than one that refuses the whole thing, so
// there is no error path: every input produces a table.

/** Delimiters we can auto-detect. */
const CANDIDATE_DELIMITERS = [',', ';', '\t', '|']

/**
 * Rows kept for rendering. The cost that matters is DOM nodes, not parsing:
 * every cell becomes an element, so a 50k-row export would build a million of
 * them and lock up the tab. Preview enough to see the shape of the data and
 * say plainly that the rest is not shown.
 */
export const MAX_PREVIEW_ROWS = 1000

export interface CsvTable {
  /** First row, treated as column names (the near-universal CSV convention). */
  header: string[]
  /** Data rows, padded to `columns` and capped at MAX_PREVIEW_ROWS. */
  rows: string[][]
  /** Widest row seen, so ragged rows still line up under the right headers. */
  columns: number
  /** Total data rows in the file, including any not kept for rendering. */
  totalRows: number
  /** True when `rows` holds fewer than `totalRows`. */
  truncated: boolean
  /** The delimiter used, as detected. */
  delimiter: string
}

/** Excel writes a UTF-8 BOM; left in place it becomes part of the first header. */
function stripBom(text: string): string {
  return text.charCodeAt(0) === 0xfeff ? text.slice(1) : text
}

/**
 * Guess the delimiter by counting candidates in the first line, ignoring any
 * inside quotes.
 *
 * Worth doing rather than assuming a comma: exporting a spreadsheet on a
 * machine with a European locale yields semicolon-separated files that are
 * still called .csv, and parsing one as comma-separated produces a single
 * column of full lines — a preview that looks broken rather than one that looks
 * unsupported.
 */
export function sniffDelimiter(text: string): string {
  const counts: Record<string, number> = {}
  for (const d of CANDIDATE_DELIMITERS) counts[d] = 0

  let inQuotes = false
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') i++ // escaped quote, stay inside
        else inQuotes = false
      }
      continue
    }
    if (ch === '"') { inQuotes = true; continue }
    if (ch === '\n' || ch === '\r') break // first line is enough to decide
    if (ch in counts) counts[ch]++
  }

  let best = ','
  for (const d of CANDIDATE_DELIMITERS) {
    if (counts[d] > counts[best]) best = d
  }
  // All zero means a single column; comma is the sane label for that.
  return counts[best] > 0 ? best : ','
}

/**
 * Parse delimited text into a table.
 *
 * Handles quoted fields containing the delimiter, newlines and doubled ("")
 * quotes, plus LF / CRLF / CR line endings.
 */
export function parseCsv(input: string, maxRows: number = MAX_PREVIEW_ROWS): CsvTable {
  const text = stripBom(input)
  const delimiter = sniffDelimiter(text)

  const kept: string[][] = []
  let totalDataRows = 0
  let columns = 0
  let header: string[] | null = null

  let field = ''
  let row: string[] = []
  let inQuotes = false

  // A row is only real if it holds content; a trailing newline (nearly every
  // CSV has one) would otherwise show up as a phantom empty row at the bottom.
  const rowHasContent = () => row.length > 1 || (row.length === 1 && row[0] !== '')

  const endRow = () => {
    row.push(field)
    field = ''
    if (rowHasContent()) {
      if (header === null) {
        header = row
        columns = Math.max(columns, row.length)
      } else {
        totalDataRows++
        if (kept.length < maxRows) {
          kept.push(row)
          columns = Math.max(columns, row.length)
        }
      }
    }
    row = []
  }

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]

    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') { field += '"'; i++ } // "" is a literal quote
        else inQuotes = false
      } else {
        field += ch
      }
      continue
    }

    // A quote only opens a quoted field at the start of one; anywhere else it
    // is a literal character. Lenient by design — `5" pipe` is not an error.
    if (ch === '"' && field === '') { inQuotes = true; continue }

    if (ch === delimiter) { row.push(field); field = ''; continue }

    if (ch === '\n') { endRow(); continue }
    if (ch === '\r') {
      if (text[i + 1] === '\n') i++ // CRLF counts once
      endRow()
      continue
    }

    field += ch
  }

  // Whatever is left when the text runs out is a final row (an unterminated
  // quote lands here too, which is why there is no error case).
  if (field !== '' || row.length > 0) endRow()

  const pad = (r: string[]) =>
    r.length === columns ? r : [...r, ...Array(columns - r.length).fill('')]

  return {
    header: header ? pad(header) : [],
    rows: kept.map(pad),
    columns,
    totalRows: totalDataRows,
    truncated: totalDataRows > kept.length,
    delimiter,
  }
}
