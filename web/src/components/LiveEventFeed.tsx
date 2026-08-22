import { useState, useMemo } from 'react';
import {
  Plus,
  Pencil,
  Trash2,
  Radio,
  ChevronDown,
  ChevronRight,
  Code2,
  FileCode,
  Search,
  Copy,
  Check,
} from 'lucide-react';
import type { EventRecord } from '../types';

interface LiveEventFeedProps {
  events: EventRecord[];
  loading: boolean;
}

function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const diff = Math.max(0, Date.now() - t);
  if (diff < 60_000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

function actionIcon(action: EventRecord['action']) {
  switch (action) {
    case 'ADDED':
      return <Plus className="h-3 w-3" />;
    case 'MODIFIED':
      return <Pencil className="h-3 w-3" />;
    case 'DELETED':
      return <Trash2 className="h-3 w-3" />;
  }
}

function actionColor(action: EventRecord['action']) {
  switch (action) {
    case 'ADDED':
      return 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30';
    case 'MODIFIED':
      return 'bg-amber-500/15 text-amber-400 border border-amber-500/30';
    case 'DELETED':
      return 'bg-red-500/15 text-red-400 border border-red-500/30';
  }
}

export function LiveEventFeed({ events, loading }: LiveEventFeedProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [filterAction, setFilterAction] = useState<string>('ALL');
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const filteredEvents = useMemo(() => {
    const q = search.toLowerCase().trim();
    return events.filter((e) => {
      if (filterAction !== 'ALL' && e.action !== filterAction) return false;
      if (q && !e.file_path.toLowerCase().includes(q) && !e.node_signature.toLowerCase().includes(q)) {
        return false;
      }
      return true;
    });
  }, [events, search, filterAction]);

  const handleCopy = (id: string, text: string, evt: React.MouseEvent) => {
    evt.stopPropagation();
    navigator.clipboard.writeText(text);
    setCopiedId(id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  return (
    <div className="panel space-y-3">
      {/* Header & Controls */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/5 pb-3">
        <div className="flex items-center gap-2">
          <Radio className="h-4 w-4 text-accent animate-pulse" />
          <h2 className="font-semibold tracking-tight text-sm flex items-center gap-2">
            Live File & Code Change Stream
            <span className="text-xs font-normal text-slate-500 font-mono">
              ({filteredEvents.length} events)
            </span>
          </h2>
        </div>

        <div className="flex items-center gap-2">
          {/* Action Filter */}
          <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
            {['ALL', 'MODIFIED', 'ADDED', 'DELETED'].map((act) => (
              <button
                key={act}
                onClick={() => setFilterAction(act)}
                className={`px-2 py-0.5 rounded text-[10px] font-medium transition-all ${
                  filterAction === act
                    ? 'bg-accent text-white shadow-sm'
                    : 'text-slate-400 hover:text-slate-200'
                }`}
              >
                {act}
              </button>
            ))}
          </div>

          {/* Search */}
          <div className="relative">
            <Search className="h-3 w-3 absolute left-2 top-1/2 -translate-y-1/2 text-slate-500" />
            <input
              type="text"
              placeholder="Filter changes…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="pl-7 pr-2.5 py-1 text-xs bg-slate-900 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent w-36 sm:w-44"
            />
          </div>
        </div>
      </div>

      {loading && <div className="text-xs text-slate-500 py-4">loading events…</div>}

      {!loading && filteredEvents.length === 0 && (
        <div className="text-xs text-slate-500 py-10 text-center">
          waiting for file changes or AST node transitions…
        </div>
      )}

      {/* Events List */}
      <ul className="divide-y divide-white/5 max-h-[500px] overflow-y-auto pr-1 space-y-1">
        {filteredEvents.map((e) => {
          const isExpanded = expandedId === e.event_id;
          const hasDiff = !!e.diff_snippet;
          const startLine = e.start_line ?? 0;
          const endLine = e.end_line ?? 0;
          const added = e.added_lines ?? 0;
          const deleted = e.deleted_lines ?? 0;

          return (
            <li
              key={e.event_id}
              onClick={() => setExpandedId(isExpanded ? null : e.event_id)}
              className="py-2.5 px-2 rounded-lg transition-all cursor-pointer hover:bg-white/[0.03] space-y-2"
            >
              <div className="flex items-start gap-2.5">
                <button className="text-slate-500 hover:text-slate-300 mt-1 shrink-0">
                  {isExpanded ? (
                    <ChevronDown className="h-3.5 w-3.5" />
                  ) : (
                    <ChevronRight className="h-3.5 w-3.5" />
                  )}
                </button>

                <span className={`chip text-[10px] px-1.5 py-0.5 rounded uppercase font-mono font-semibold flex items-center gap-1 shrink-0 ${actionColor(e.action)}`}>
                  {actionIcon(e.action)}
                  {e.action}
                </span>

                <div className="min-w-0 flex-1 space-y-0.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-xs font-medium text-slate-200 truncate" title={e.node_signature}>
                      {e.node_signature}
                    </span>
                    {startLine > 0 && (
                      <span className="text-[10px] font-mono text-slate-400 bg-slate-800/80 px-1 py-0.2 rounded border border-white/5 shrink-0">
                        L{startLine}{endLine > startLine ? `-L${endLine}` : ''}
                      </span>
                    )}
                  </div>

                  <div className="flex items-center gap-2 text-[11px] text-slate-400">
                    <span className="flex items-center gap-1 font-mono truncate text-slate-500" title={e.file_path}>
                      <FileCode className="h-3 w-3 text-cyan-400 shrink-0" />
                      {e.file_path}
                    </span>
                    <span className="text-slate-600">·</span>
                    <span className="text-[10px] text-slate-400">{e.node_type}</span>
                  </div>
                </div>

                {/* Line change counters */}
                <div className="flex items-center gap-2 shrink-0 text-right">
                  {(added > 0 || deleted > 0) && (
                    <div className="flex items-center gap-1 text-[11px] font-mono font-medium">
                      {added > 0 && <span className="text-emerald-400">+{added}</span>}
                      {deleted > 0 && <span className="text-red-400">-{deleted}</span>}
                    </div>
                  )}
                  <span className="text-[10px] text-slate-500 font-mono">
                    {relativeTime(e.event_time)}
                  </span>
                </div>
              </div>

              {/* Expandable Line-by-Line Code Diff Box */}
              {isExpanded && (
                <div
                  onClick={(evt) => evt.stopPropagation()}
                  className="mt-2 ml-6 rounded-lg bg-[#0a0e14] border border-white/10 overflow-hidden text-xs"
                >
                  <div className="px-3 py-1.5 bg-slate-900/90 border-b border-white/5 flex items-center justify-between">
                    <div className="flex items-center gap-2 text-[11px] text-slate-400 font-mono">
                      <Code2 className="h-3.5 w-3.5 text-accent" />
                      <span>Code Diff ({e.action})</span>
                      {startLine > 0 && <span>· Lines {startLine} - {endLine}</span>}
                    </div>
                    {hasDiff && (
                      <button
                        onClick={(evt) => handleCopy(e.event_id, e.diff_snippet ?? '', evt)}
                        className="flex items-center gap-1 text-[10px] text-slate-400 hover:text-white px-2 py-0.5 rounded bg-white/5 hover:bg-white/10 border border-white/5"
                      >
                        {copiedId === e.event_id ? (
                          <>
                            <Check className="h-3 w-3 text-emerald-400" />
                            <span>Copied</span>
                          </>
                        ) : (
                          <>
                            <Copy className="h-3 w-3" />
                            <span>Copy Diff</span>
                          </>
                        )}
                      </button>
                    )}
                  </div>

                  <div className="p-3 max-h-64 overflow-y-auto font-mono text-[11px] leading-relaxed space-y-0.5 select-text">
                    {hasDiff ? (
                      e.diff_snippet?.split('\n').map((line, idx) => {
                        const isAdd = line.startsWith('+ ');
                        const isDel = line.startsWith('- ');
                        const lineClass = isAdd
                          ? 'text-emerald-400 bg-emerald-500/10 -mx-3 px-3 py-0.5 border-l-2 border-emerald-500'
                          : isDel
                          ? 'text-red-400 bg-red-500/10 -mx-3 px-3 py-0.5 border-l-2 border-red-500'
                          : 'text-slate-300 px-1 py-0.5';

                        return (
                          <div key={idx} className={lineClass}>
                            {line}
                          </div>
                        );
                      })
                    ) : (
                      <div className="text-slate-500 italic">
                        No inline code snippet available for this event.
                      </div>
                    )}
                  </div>
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
