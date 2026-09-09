// Copy text to the clipboard, reporting honest success.
//
// The Clipboard API exists only in secure contexts (https or localhost); the
// dashboard is routinely served over plain HTTP on configurable addresses
// where navigator.clipboard is undefined and a raw call throws a TypeError —
// and writeText() itself rejects in ordinary use (Chrome refuses when the
// document is not focused). The copy buttons used to dereference
// navigator.clipboard directly and flash "Copied" unconditionally, so
// failures either crashed the handler or lied about success.
export async function copyToClipboard(text: string): Promise<boolean> {
  const nav = (globalThis as { navigator?: { clipboard?: { writeText?: (t: string) => Promise<void> } } }).navigator;
  if (!nav?.clipboard?.writeText) {
    return false;
  }
  try {
    await nav.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
