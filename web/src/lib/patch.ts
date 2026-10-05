// Build an applyable .patch file from a WrongTrace diff_snippet.
//
// internal/ast stores diff_snippet in a DISPLAY form: every content line is
// prefixed with a two-character marker ("+ ", "- ", "  "), and the stream can
// also carry non-content marker lines ("… unchanged lines omitted …" and the
// truncation notice). A unified diff's hunk header must declare exactly how many
// old and new lines its body contains, because that is how a patch reader bounds
// the body. The viewer previously wrote a hardcoded "@@ -1 +1 @@" -- one old
// line, one new line -- for every diff, so any multi-line snippet overflowed the
// declared count and every "Download as .patch file" export was rejected
// outright as a corrupt patch. Deriving the header from the body makes the
// counts agree by construction.
//
// Scope note: this makes the envelope structurally valid. diff_snippet is a
// per-declaration fragment, not a whole-file diff, and its omission markers are
// placeholders rather than real file context, so a full context apply against a
// live checkout is a separate concern; the contract pinned here is that the
// header never contradicts the body it introduces.
export function buildUnifiedPatch(diff: string, filePath?: string): string {
  const path = filePath && filePath.length > 0 ? filePath : 'unknown';
  const body = diff.split('\n');

  // Counts follow the same first-character convention the body uses: "+" lines
  // exist only on the new side, "-" only on the old side, and every other line
  // (context and the omission/truncation markers) is present on both.
  let oldCount = 0;
  let newCount = 0;
  for (const line of body) {
    if (line.startsWith('-')) {
      oldCount += 1;
    } else if (line.startsWith('+')) {
      newCount += 1;
    } else {
      oldCount += 1;
      newCount += 1;
    }
  }

  return `--- a/${path}\n+++ b/${path}\n@@ -1,${oldCount} +1,${newCount} @@\n${diff}\n`;
}
