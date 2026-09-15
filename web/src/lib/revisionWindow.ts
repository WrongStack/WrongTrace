// Pure windowing for FileHistoryTimeline. `revisions` is chronological
// (index 0 = oldest). The visible window is always the NEWEST `visibleCount`
// revisions, so the latest ("Current") revision is never cut off and the
// "Show older" control only ever reveals older history, in either sort order.

export interface RevisionWindow<T> {
  displayed: T[];
  hiddenCount: number;
}

export function selectRevisionWindow<T>(revisions: T[], visibleCount: number, newestFirst: boolean): RevisionWindow<T> {
  const start = Math.max(0, revisions.length - Math.max(0, visibleCount));
  const capped = revisions.slice(start);
  return {
    displayed: newestFirst ? [...capped].reverse() : capped,
    hiddenCount: start,
  };
}

// The smallest visibleCount (never shrinking the current one) that renders the
// revision with `key`; unknown keys leave the count unchanged.
export function visibleCountToInclude<T extends { key: string }>(revisions: T[], key: string, visibleCount: number): number {
  const idx = revisions.findIndex((r) => r.key === key);
  if (idx < 0) return visibleCount;
  return Math.max(visibleCount, revisions.length - idx);
}
