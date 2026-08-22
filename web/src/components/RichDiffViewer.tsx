import { useState, useMemo } from 'react';
import { Copy, Check, Download, Columns, AlignLeft, FileCode, WrapText } from 'lucide-react';

interface RichDiffViewerProps {
  diff?: string | null;
  filePath?: string;
  signature?: string;
  action?: string;
  startLine?: number;
  endLine?: number;
  initialMode?: 'unified' | 'split' | 'raw';
  maxHeight?: string;
}

interface SplitDiffRow {
  leftNum?: number;
  leftText?: string;
  leftType?: 'del' | 'context' | 'empty';
  rightNum?: number;
  rightText?: string;
  rightType?: 'add' | 'context' | 'empty';
}

export function RichDiffViewer({
  diff,
  filePath,
  signature,
  action,
  startLine,
  endLine,
  initialMode = 'unified',
  maxHeight = '500px',
}: RichDiffViewerProps) {
  const [viewMode, setViewMode] = useState<'unified' | 'split' | 'raw'>(initialMode);
  const [wordWrap, setWordWrap] = useState(false);
  const [copied, setCopied] = useState(false);

  // Parse diff stats & lines
  const { addedCount, deletedCount, unifiedLines, splitRows } = useMemo(() => {
    if (!diff) {
      return { addedCount: 0, deletedCount: 0, unifiedLines: [], splitRows: [] };
    }

    const rawLines = diff.split('\n');
    let added = 0;
    let deleted = 0;

    const uLines = rawLines.map((line, idx) => {
      let type: 'add' | 'del' | 'context' = 'context';
      if (line.startsWith('+ ') || line.startsWith('+')) {
        type = 'add';
        added++;
      } else if (line.startsWith('- ') || line.startsWith('-')) {
        type = 'del';
        deleted++;
      }
      return { num: idx + 1, text: line, type };
    });

    // Build side-by-side split rows
    const sRows: SplitDiffRow[] = [];
    let lLine = 1;
    let rLine = 1;

    let pendingDels: string[] = [];
    let pendingAdds: string[] = [];

    const flushPending = () => {
      const maxLen = Math.max(pendingDels.length, pendingAdds.length);
      for (let i = 0; i < maxLen; i++) {
        const d = pendingDels[i];
        const a = pendingAdds[i];
        sRows.push({
          leftNum: d !== undefined ? lLine++ : undefined,
          leftText: d !== undefined ? d : '',
          leftType: d !== undefined ? 'del' : 'empty',
          rightNum: a !== undefined ? rLine++ : undefined,
          rightText: a !== undefined ? a : '',
          rightType: a !== undefined ? 'add' : 'empty',
        });
      }
      pendingDels = [];
      pendingAdds = [];
    };

    rawLines.forEach((line) => {
      if (line.startsWith('- ') || line.startsWith('-')) {
        pendingDels.push(line);
      } else if (line.startsWith('+ ') || line.startsWith('+')) {
        pendingAdds.push(line);
      } else {
        flushPending();
        sRows.push({
          leftNum: lLine++,
          leftText: line,
          leftType: 'context',
          rightNum: rLine++,
          rightText: line,
          rightType: 'context',
        });
      }
    });
    flushPending();

    return { addedCount: added, deletedCount: deleted, unifiedLines: uLines, splitRows: sRows };
  }, [diff]);

  const handleCopy = () => {
    if (!diff) return;
    navigator.clipboard.writeText(diff);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleDownloadPatch = () => {
    if (!diff) return;
    const header = `--- a/${filePath || 'unknown'}\n+++ b/${filePath || 'unknown'}\n@@ -1 +1 @@\n`;
    const patchContent = header + diff;
    const blob = new Blob([patchContent], { type: 'text/x-diff;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${(filePath || 'diff').replace(/[^a-zA-Z0-9_-]/g, '_')}.patch`;
    a.click();
    URL.revokeObjectURL(url);
  };

  if (!diff) {
    return (
      <div className="flex flex-col items-center justify-center p-8 text-slate-500 text-xs gap-1.5 border border-white/5 rounded-xl bg-slate-950/40">
        <FileCode className="h-5 w-5 text-slate-600" />
        <span>No diff snippet recorded for this change.</span>
      </div>
    );
  }

  return (
    <div className="border border-white/10 rounded-xl bg-slate-950/90 overflow-hidden shadow-lg flex flex-col">
      {/* Diff Toolbar Header */}
      <div className="px-3 py-2 bg-slate-900/80 border-b border-white/10 flex flex-wrap items-center justify-between gap-2 text-xs">
        <div className="flex items-center gap-2 font-mono">
          {action && (
            <span
              className={`px-2 py-0.5 rounded text-[10px] font-bold ${
                action === 'ADDED'
                  ? 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30'
                  : action === 'MODIFIED'
                  ? 'bg-amber-500/15 text-amber-400 border border-amber-500/30'
                  : 'bg-red-500/15 text-red-400 border border-red-500/30'
              }`}
            >
              {action}
            </span>
          )}

          {/* Line Stats */}
          <div className="flex items-center gap-1.5 text-[11px]">
            <span className="text-emerald-400 font-semibold">+{addedCount}</span>
            <span className="text-red-400 font-semibold">-{deletedCount}</span>
            {startLine ? (
              <span className="text-slate-500">· L{startLine}-{endLine}</span>
            ) : null}
          </div>
        </div>

        {/* View Mode Controls & Actions */}
        <div className="flex items-center gap-1.5">
          {/* Mode switch */}
          <div className="flex items-center bg-slate-950 border border-white/10 rounded-lg p-0.5">
            <button
              type="button"
              onClick={() => setViewMode('unified')}
              className={`px-2 py-0.5 rounded text-[11px] font-medium flex items-center gap-1 transition-all ${
                viewMode === 'unified' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
              }`}
              title="Unified Inline Diff"
            >
              <AlignLeft className="h-3 w-3" />
              Unified
            </button>
            <button
              type="button"
              onClick={() => setViewMode('split')}
              className={`px-2 py-0.5 rounded text-[11px] font-medium flex items-center gap-1 transition-all ${
                viewMode === 'split' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
              }`}
              title="Side-by-Side Split Diff"
            >
              <Columns className="h-3 w-3" />
              Split
            </button>
            <button
              type="button"
              onClick={() => setViewMode('raw')}
              className={`px-2 py-0.5 rounded text-[11px] font-medium transition-all ${
                viewMode === 'raw' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
              }`}
              title="Raw Plain Text"
            >
              Raw
            </button>
          </div>

          {/* Word Wrap Toggle */}
          <button
            type="button"
            onClick={() => setWordWrap(!wordWrap)}
            className={`p-1.5 rounded-lg border border-white/10 transition-all ${
              wordWrap ? 'bg-accent/20 text-accent border-accent/40' : 'bg-slate-900 text-slate-400 hover:text-white'
            }`}
            title={wordWrap ? 'Disable Word Wrap' : 'Enable Word Wrap'}
          >
            <WrapText className="h-3.5 w-3.5" />
          </button>

          {/* Download Patch */}
          <button
            type="button"
            onClick={handleDownloadPatch}
            className="p-1.5 rounded-lg bg-slate-900 hover:bg-slate-800 border border-white/10 text-slate-400 hover:text-white transition-all"
            title="Download as .patch file"
          >
            <Download className="h-3.5 w-3.5" />
          </button>

          {/* Copy Button */}
          <button
            type="button"
            onClick={handleCopy}
            className="flex items-center gap-1 px-2.5 py-1 text-[11px] bg-slate-900 hover:bg-slate-800 border border-white/10 rounded-lg text-slate-300 transition-all"
          >
            {copied ? (
              <>
                <Check className="h-3 w-3 text-emerald-400" />
                <span className="text-emerald-400">Copied</span>
              </>
            ) : (
              <>
                <Copy className="h-3 w-3" />
                <span>Copy</span>
              </>
            )}
          </button>
        </div>
      </div>

      {/* Diff Content Container */}
      <div
        className="overflow-auto font-mono text-xs select-text"
        style={{ maxHeight }}
      >
        {viewMode === 'unified' && (
          <div className={`p-2 space-y-0.5 ${wordWrap ? 'whitespace-pre-wrap' : 'whitespace-pre'}`}>
            {unifiedLines.map((line) => {
              const isAdd = line.type === 'add';
              const isDel = line.type === 'del';
              return (
                <div
                  key={line.num}
                  className={`flex items-start px-2 py-0.5 rounded leading-relaxed transition-colors ${
                    isAdd
                      ? 'bg-emerald-950/40 text-emerald-300 border-l-2 border-emerald-500'
                      : isDel
                      ? 'bg-red-950/40 text-red-300 border-l-2 border-red-500'
                      : 'text-slate-300 hover:bg-white/5'
                  }`}
                >
                  <span className="inline-block w-8 text-slate-600 select-none text-right shrink-0 pr-3">
                    {line.num}
                  </span>
                  <span className="inline-block w-4 text-slate-500 select-none shrink-0 font-bold">
                    {isAdd ? '+' : isDel ? '-' : ' '}
                  </span>
                  <span className="flex-1 break-all">{line.text.replace(/^[+-]\s?/, '')}</span>
                </div>
              );
            })}
          </div>
        )}

        {viewMode === 'split' && (
          <div className="grid grid-cols-2 divide-x divide-white/10 min-w-[600px]">
            {/* Left Side (Original / Deletions) */}
            <div className="p-2 space-y-0.5 bg-slate-950/60">
              <div className="text-[10px] text-slate-500 uppercase font-semibold pb-1 px-2 border-b border-white/5">
                Original
              </div>
              {splitRows.map((row, idx) => (
                <div
                  key={`l-${idx}`}
                  className={`flex items-start px-1.5 py-0.5 rounded leading-relaxed min-h-[22px] ${
                    row.leftType === 'del'
                      ? 'bg-red-950/50 text-red-300 border-l-2 border-red-500'
                      : row.leftType === 'empty'
                      ? 'bg-slate-950/30'
                      : 'text-slate-300 hover:bg-white/5'
                  }`}
                >
                  <span className="inline-block w-6 text-slate-600 select-none text-right shrink-0 pr-2">
                    {row.leftNum || ''}
                  </span>
                  <span className="flex-1 truncate">{row.leftText ? row.leftText.replace(/^-\s?/, '') : ''}</span>
                </div>
              ))}
            </div>

            {/* Right Side (Modified / Additions) */}
            <div className="p-2 space-y-0.5 bg-slate-950/60">
              <div className="text-[10px] text-slate-500 uppercase font-semibold pb-1 px-2 border-b border-white/5">
                Modified
              </div>
              {splitRows.map((row, idx) => (
                <div
                  key={`r-${idx}`}
                  className={`flex items-start px-1.5 py-0.5 rounded leading-relaxed min-h-[22px] ${
                    row.rightType === 'add'
                      ? 'bg-emerald-950/50 text-emerald-300 border-l-2 border-emerald-500'
                      : row.rightType === 'empty'
                      ? 'bg-slate-950/30'
                      : 'text-slate-300 hover:bg-white/5'
                  }`}
                >
                  <span className="inline-block w-6 text-slate-600 select-none text-right shrink-0 pr-2">
                    {row.rightNum || ''}
                  </span>
                  <span className="flex-1 truncate">{row.rightText ? row.rightText.replace(/^\+\s?/, '') : ''}</span>
                </div>
              ))}
            </div>
          </div>
        )}

        {viewMode === 'raw' && (
          <div className="p-4 bg-slate-950 font-mono text-xs text-slate-300 whitespace-pre overflow-x-auto leading-relaxed">
            {diff}
          </div>
        )}
      </div>
    </div>
  );
}
