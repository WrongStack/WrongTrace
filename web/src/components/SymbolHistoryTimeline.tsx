import { useState } from 'react';
import { History, Bot, GitCommit, Plus, Minus, Hash, DollarSign, Clock, Sparkles, ChevronDown, ChevronRight, FileCode, CheckCircle2 } from 'lucide-react';
import { useSymbolHistory } from '../hooks/useMetrics';
import { RichDiffViewer } from './RichDiffViewer';
import type { SymbolHistoryRecord } from '../types';

interface SymbolHistoryTimelineProps {
  filePath?: string;
  signature: string;
}

function fmtUSD(n: number): string {
  if (!n || n <= 0) return '$0.0000';
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
}

function fmtRelativeTime(isoStr: string): string {
  const t = Date.parse(isoStr);
  if (Number.isNaN(t)) return isoStr;
  const diffSec = Math.round((Date.now() - t) / 1000);
  if (diffSec < 60) return `${diffSec}s ago`;
  const diffMin = Math.round(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHour = Math.round(diffMin / 60);
  if (diffHour < 24) return `${diffHour}h ago`;
  const diffDay = Math.round(diffHour / 24);
  return `${diffDay}d ago`;
}

export function SymbolHistoryTimeline({ filePath = '', signature }: SymbolHistoryTimelineProps) {
  const { data: history = [], isLoading } = useSymbolHistory(filePath, signature);
  const [expandedIndex, setExpandedIndex] = useState<number | null>(null);

  if (isLoading) {
    return (
      <div className="p-4 rounded-xl bg-slate-950/60 border border-white/5 flex items-center justify-center text-xs text-slate-400 gap-2">
        <History className="h-4 w-4 animate-spin text-purple-400" />
        <span>Loading symbol lineage & history...</span>
      </div>
    );
  }

  if (history.length === 0) {
    return (
      <div className="p-3 rounded-xl bg-slate-950/40 border border-white/5 space-y-1 text-xs">
        <div className="flex items-center justify-between text-slate-400">
          <span className="flex items-center gap-1.5 font-medium text-slate-300">
            <History className="h-3.5 w-3.5 text-purple-400" />
            Symbol Lineage & History
          </span>
          <span className="text-[10px] font-mono text-slate-500">1 version</span>
        </div>
        <p className="text-[11px] text-slate-400">
          Initial baseline snapshot. No subsequent AST mutation events recorded yet.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3 p-3.5 rounded-xl bg-slate-950/80 border border-purple-500/20 shadow-inner">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-white/10 pb-2.5">
        <div className="flex items-center gap-2">
          <div className="p-1 rounded-lg bg-purple-500/20 text-purple-400">
            <History className="h-4 w-4" />
          </div>
          <div>
            <h4 className="font-semibold text-xs text-white flex items-center gap-1.5">
              AST Symbol Evolution & Lineage
            </h4>
            <div className="text-[10px] text-slate-400 font-mono">
              Chronological mutation timeline & model attribution
            </div>
          </div>
        </div>
        <span className="chip bg-purple-500/15 text-purple-300 border border-purple-500/30 text-[11px] font-mono font-bold">
          {history.length} Revisions
        </span>
      </div>

      {/* Timeline entries */}
      <div className="space-y-2.5 relative before:absolute before:left-3 before:top-2 before:bottom-2 before:w-0.5 before:bg-gradient-to-b before:from-purple-500/50 before:via-indigo-500/30 before:to-slate-800">
        {history.map((rev: SymbolHistoryRecord, idx: number) => {
          const isExpanded = expandedIndex === idx || history.length <= 2;
          const isLatest = idx === history.length - 1;
          const versionLabel = `v${idx + 1}`;

          const actionBg =
            rev.action === 'ADDED'
              ? 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30'
              : rev.action === 'DELETED'
              ? 'bg-rose-500/20 text-rose-400 border-rose-500/30'
              : 'bg-amber-500/20 text-amber-400 border-amber-500/30';

          return (
            <div
              key={rev.event_id || idx}
              className={`relative pl-7 transition-all rounded-lg p-2 ${
                isLatest
                  ? 'bg-purple-950/20 border border-purple-500/30 shadow-md'
                  : 'bg-slate-900/40 border border-white/5'
              }`}
            >
              {/* Timeline Node Dot */}
              <div
                className={`absolute left-1.5 top-3 w-3 h-3 rounded-full border-2 transform -translate-x-1/2 flex items-center justify-center ${
                  isLatest
                    ? 'bg-purple-500 border-purple-200 ring-2 ring-purple-500/40'
                    : 'bg-slate-900 border-indigo-400'
                }`}
              />

              {/* Revision Header */}
              <div
                onClick={() => setExpandedIndex(expandedIndex === idx ? null : idx)}
                className="flex items-center justify-between cursor-pointer group"
              >
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-mono font-bold text-xs text-purple-300 bg-purple-500/10 px-1.5 py-0.5 rounded border border-purple-500/20">
                    {versionLabel}
                  </span>
                  <span className={`text-[10px] font-mono font-bold px-1.5 py-0.5 rounded border ${actionBg}`}>
                    {rev.action}
                  </span>
                  {isLatest && (
                    <span className="text-[10px] font-mono px-1 rounded bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 flex items-center gap-1">
                      <CheckCircle2 className="h-2.5 w-2.5" /> Current
                    </span>
                  )}
                  <span className="text-xs font-semibold text-slate-200 flex items-center gap-1">
                    <Bot className="h-3 w-3 text-cyan-400" />
                    {rev.model_name || 'unknown-model'}
                  </span>
                </div>

                <div className="flex items-center gap-2 text-slate-400 text-[11px] font-mono">
                  <span>{fmtRelativeTime(rev.event_time)}</span>
                  {isExpanded ? (
                    <ChevronDown className="h-3.5 w-3.5 text-slate-400 group-hover:text-white" />
                  ) : (
                    <ChevronRight className="h-3.5 w-3.5 text-slate-400 group-hover:text-white" />
                  )}
                </div>
              </div>

              {/* Revision Details (when expanded or short history) */}
              {isExpanded && (
                <div className="mt-2.5 space-y-2 text-xs border-t border-white/5 pt-2">
                  {/* Intent / Task */}
                  {rev.intent && (
                    <div className="bg-slate-950 p-2 rounded border border-white/5 text-slate-300 text-[11px] italic">
                      "{rev.intent}"
                    </div>
                  )}

                  {/* Metrics Row */}
                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-1.5 text-[11px] font-mono">
                    <div className="panel-raised p-1.5">
                      <div className="text-slate-500 text-[10px]">LOC</div>
                      <div className="text-slate-200 font-semibold">{rev.lines_of_code || 0} lines</div>
                    </div>
                    <div className="panel-raised p-1.5">
                      <div className="text-slate-500 text-[10px]">Delta</div>
                      <div className="flex items-center gap-1.5">
                        <span className="text-emerald-400">+{rev.added_lines || 0}</span>
                        <span className="text-rose-400">-{rev.deleted_lines || 0}</span>
                      </div>
                    </div>
                    <div className="panel-raised p-1.5">
                      <div className="text-slate-500 text-[10px]">Tokens</div>
                      <div className="text-purple-300 font-medium">
                        {(rev.prompt_tokens + rev.completion_tokens).toLocaleString()}
                      </div>
                    </div>
                    <div className="panel-raised p-1.5">
                      <div className="text-slate-500 text-[10px]">Cost</div>
                      <div className="text-emerald-400 font-medium">{fmtUSD(rev.cost_usd)}</div>
                    </div>
                  </div>

                  {/* AST Hash */}
                  {rev.ast_content_hash && (
                    <div className="flex items-center gap-1.5 text-[10px] text-slate-500 font-mono">
                      <Hash className="h-3 w-3 text-slate-600" />
                      <span>AST Hash: {rev.ast_content_hash.slice(0, 16)}...</span>
                    </div>
                  )}

                  {/* Diff Snippet */}
                  {rev.diff_snippet && (
                    <div className="mt-2">
                      <div className="text-[10px] text-slate-400 mb-1 font-mono flex items-center justify-between">
                        <span>Code Mutation Diff ({rev.action})</span>
                        <span className="text-slate-500">
                          L{rev.start_line} - L{rev.end_line}
                        </span>
                      </div>
                      <RichDiffViewer
                        diff={rev.diff_snippet}
                        filePath={rev.file_path}
                        signature={rev.node_signature}
                        action={rev.action}
                        startLine={rev.start_line}
                        endLine={rev.end_line}
                        maxHeight="160px"
                      />
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
