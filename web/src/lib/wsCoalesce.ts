// Adaptive WebSocket refresh coalescing for the Dashboard.
//
// A window is armed by the first frame after the previous flush, so the frame
// count at arming time is always ~1 — the delay must be chosen from the
// PREVIOUS window's traffic. Its frame count is spread over the time from that
// window's start until now, which both measures the sustained rate and decays
// it naturally: a busy window followed by a quiet gap snaps back to the
// minimum.

export const WS_COALESCE_MIN_MS = 250;
export const WS_COALESCE_MID_MS = 500;
export const WS_COALESCE_MAX_MS = 1000;

export function nextCoalesceMs(prevWindowFrames: number, msSincePrevWindowStart: number): number {
  if (!(prevWindowFrames > 0) || !(msSincePrevWindowStart > 0)) return WS_COALESCE_MIN_MS;
  const perSecond = (prevWindowFrames * 1000) / msSincePrevWindowStart;
  if (perSecond > 20) return WS_COALESCE_MAX_MS;
  if (perSecond > 5) return WS_COALESCE_MID_MS;
  return WS_COALESCE_MIN_MS;
}
