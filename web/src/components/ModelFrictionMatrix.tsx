import { useState, useMemo } from 'react';
import {
  Swords,
  Flame,
  Bot,
  AlertTriangle,
  ArrowRight,
  Clock,
  FileCode,
  Layers,
  Sparkles,
  Search,
  Filter,
  ChevronDown,
  ChevronRight,
  TrendingDown,
  Repeat,
} from 'lucide-react';
import { useModelFriction } from '../hooks/useMetrics';
import { RichDiffViewer } from './RichDiffViewer';
import type { CrossThrashEvent, ModelFrictionEdge } from '../types';

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

function fmtDeltaDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s later`;
  const min = Math.round(seconds / 60);
  if (min < 60) return `${min}m later`;
  const hrs = Math.round(min / 60);
  if (hrs < 24) return `${hrs}h later`;
  const days = Math.round(hrs / 24);
  return `${days}d later`;
}

export function ModelFrictionMatrix() {
  const { data: frictionReport, isLoading } = useModelFriction(250);
  const [filterMode, setFilterMode] = useState<'all' | 'cross_only' | 'self_only'>('all');
  const [selectedPair, setSelectedPair] = useState<{ author: string; overwriter: string } | null>(null);
  const [searchQuery, setSearchQuery] = useState('');
  const [expandedEventId, setExpandedEventId] = useState<string | null>(null);

  const edges = frictionReport?.edges ?? [];
  const rawCollisions = frictionReport?.recent_collisions ?? [];

  // Extract unique model list
  const uniqueModels = useMemo(() => {
    const set = new Set<string>();
    edges.forEach((e) => {
      if (e.author_model && e.author_model !== 'unknown') set.add(e.author_model);
      if (e.overwriter_model && e.overwriter_model !== 'unknown') set.add(e.overwriter_model);
    });
    return Array.from(set).sort();
  }, [edges]);

  // Matrix cell lookup: map["author->overwriter"] = edge
  const edgeMatrix = useMemo(() => {
    const map = new Map<string, ModelFrictionEdge>();
    edges.forEach((e) => {
      map.set(`${e.author_model}->${e.overwriter_model}`, e);
    });
    return map;
  }, [edges]);

  // Filter collisions
  const filteredCollisions = useMemo(() => {
    const q = searchQuery.toLowerCase().trim();
    return rawCollisions.filter((c) => {
      if (filterMode === 'cross_only' && !c.is_cross_agent) return false;
      if (filterMode === 'self_only' && c.is_cross_agent) return false;
      if (
        selectedPair &&
        (c.author_model !== selectedPair.author || c.overwriter_model !== selectedPair.overwriter)
      ) {
        return false;
      }
      if (
        q &&
        !c.file_path.toLowerCase().includes(q) &&
        !c.node_signature.toLowerCase().includes(q) &&
        !c.author_model.toLowerCase().includes(q) &&
        !c.overwriter_model.toLowerCase().includes(q)
      ) {
        return false;
      }
      return true;
    });
  }, [rawCollisions, filterMode, selectedPair, searchQuery]);

  return (
    <div className="panel space-y-5 bg-gradient-to-b from-slate-900/95 via-slate-950/90 to-slate-900/95 border border-rose-500/20 shadow-2xl rounded-2xl p-5">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-white/10 pb-4">
        <div className="flex items-center gap-3">
          <div className="p-2.5 rounded-xl bg-gradient-to-br from-rose-500/20 to-amber-500/20 text-rose-400 border border-rose-500/30 shadow-inner">
            <Swords className="h-5 w-5" />
          </div>
          <div>
            <h2 className="font-bold tracking-tight text-base text-white flex items-center gap-2">
              Inter-Agent Friction & Cross-Thrashing Matrix
              <span className="text-xs font-normal text-rose-400">
                (Who Overwrites Whose Code?)
              </span>
            </h2>
            <p className="text-xs text-slate-400">
              Detects multi-agent code collisions, inter-model rewrites, and wasted AST refactoring friction.
            </p>
          </div>
        </div>

        {/* Filter Controls */}
        <div className="flex flex-wrap items-center gap-2.5">
          <div className="relative">
            <Search className="h-3.5 w-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
            <input
              type="text"
              placeholder="Search conflicts or symbols…"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-8 pr-3 py-1.5 text-xs bg-slate-950 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent w-48 sm:w-60"
            />
          </div>

          <div className="flex items-center bg-slate-950 border border-white/10 rounded-lg p-0.5 text-xs">
            <button
              onClick={() => {
                setFilterMode('all');
                setSelectedPair(null);
              }}
              className={`px-2.5 py-1 rounded font-medium transition-all ${
                filterMode === 'all' && !selectedPair
                  ? 'bg-rose-500 text-white font-semibold shadow-sm'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              All Events
            </button>
            <button
              onClick={() => {
                setFilterMode('cross_only');
                setSelectedPair(null);
              }}
              className={`px-2.5 py-1 rounded font-medium transition-all ${
                filterMode === 'cross_only'
                  ? 'bg-rose-500 text-white font-semibold shadow-sm'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              ⚔️ Cross-Model Only
            </button>
            <button
              onClick={() => {
                setFilterMode('self_only');
                setSelectedPair(null);
              }}
              className={`px-2.5 py-1 rounded font-medium transition-all ${
                filterMode === 'self_only'
                  ? 'bg-amber-500 text-white font-semibold shadow-sm'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              🔄 Self-Thrash
            </button>
          </div>
        </div>
      </div>

      {/* Summary KPI Strip */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <div className="panel-raised p-3 border-l-2 border-l-rose-500 bg-slate-950/60 shadow-md">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Total Overwrites</span>
            <Swords className="h-3.5 w-3.5 text-rose-400" />
          </div>
          <div className="font-mono text-lg font-bold text-rose-400 mt-1">
            {frictionReport?.total_collisions ?? 0}
          </div>
          <div className="text-[10px] text-slate-500 font-mono">AST code replacements</div>
        </div>

        <div className="panel-raised p-3 border-l-2 border-l-amber-500 bg-slate-950/60 shadow-md">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Cross-Agent Collision Rate</span>
            <AlertTriangle className="h-3.5 w-3.5 text-amber-400" />
          </div>
          <div className="font-mono text-lg font-bold text-amber-300 mt-1">
            {(frictionReport?.cross_agent_ratio_pct ?? 0).toFixed(1)}%
          </div>
          <div className="text-[10px] text-slate-500 font-mono">Inter-model rewrites</div>
        </div>

        <div className="panel-raised p-3 border-l-2 border-l-purple-500 bg-slate-950/60 shadow-md">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Active Agent Models</span>
            <Bot className="h-3.5 w-3.5 text-purple-400" />
          </div>
          <div className="font-mono text-lg font-bold text-purple-300 mt-1">
            {uniqueModels.length}
          </div>
          <div className="text-[10px] text-slate-500 font-mono">Monitored models</div>
        </div>

        <div className="panel-raised p-3 border-l-2 border-l-cyan-500 bg-slate-950/60 shadow-md">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Top Friction Vector</span>
            <Flame className="h-3.5 w-3.5 text-cyan-400" />
          </div>
          <div className="font-mono text-xs font-bold text-cyan-300 mt-1 truncate" title={frictionReport?.top_friction_pair || 'None'}>
            {frictionReport?.top_friction_pair || 'No collisions yet'}
          </div>
          <div className="text-[10px] text-slate-500 font-mono">Highest friction pair</div>
        </div>
      </div>

      {/* Main Content: 2-Column Split (Matrix Heatmap Left, Live Conflict Stream Right) */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-5">
        {/* Left: Interactive Collision Matrix Grid */}
        <div className="lg:col-span-6 panel p-4 bg-slate-950/90 border border-white/10 rounded-xl space-y-3">
          <div className="flex items-center justify-between border-b border-white/5 pb-2">
            <div className="flex items-center gap-2 text-xs font-semibold text-white">
              <Layers className="h-4 w-4 text-rose-400" />
              <span>Model-to-Model Collision Matrix</span>
            </div>
            <span className="text-[10px] font-mono text-slate-500">Rows: Author ➔ Cols: Overwriter</span>
          </div>

          {uniqueModels.length === 0 ? (
            <div className="py-12 text-center text-xs text-slate-500">
              No inter-agent collisions recorded yet. Once multiple AI models modify the same symbols, the friction matrix will emerge.
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-center text-xs border-collapse font-mono">
                <thead>
                  <tr>
                    <th className="p-2 text-left text-slate-500 text-[10px] border-b border-white/10">
                      Author \ Overwriter
                    </th>
                    {uniqueModels.map((m) => (
                      <th
                        key={m}
                        className="p-2 text-slate-300 font-bold text-[10px] border-b border-white/10 max-w-[90px] truncate"
                        title={m}
                      >
                        {m.length > 12 ? `${m.slice(0, 10)}…` : m}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-white/5">
                  {uniqueModels.map((author) => (
                    <tr key={author} className="hover:bg-white/[0.02]">
                      <td className="p-2 text-left font-bold text-slate-300 text-[11px] truncate max-w-[120px]" title={author}>
                        {author}
                      </td>
                      {uniqueModels.map((overwriter) => {
                        const edge = edgeMatrix.get(`${author}->${overwriter}`);
                        const count = edge?.conflict_count ?? 0;
                        const isSelf = author === overwriter;
                        const isSelected =
                          selectedPair?.author === author && selectedPair?.overwriter === overwriter;

                        let cellBg = 'bg-slate-900/40 text-slate-600';
                        if (count > 0) {
                          if (isSelf) {
                            cellBg = 'bg-amber-500/15 text-amber-300 border border-amber-500/30 hover:bg-amber-500/25';
                          } else if (count >= 5) {
                            cellBg = 'bg-rose-500/30 text-rose-300 border border-rose-500/50 hover:bg-rose-500/40 font-bold shadow-lg shadow-rose-500/10';
                          } else {
                            cellBg = 'bg-rose-500/15 text-rose-300 border border-rose-500/30 hover:bg-rose-500/25';
                          }
                        }

                        if (isSelected) {
                          cellBg = 'ring-2 ring-white bg-rose-500 text-white font-bold';
                        }

                        return (
                          <td key={overwriter} className="p-1">
                            <button
                              type="button"
                              onClick={() => {
                                if (count > 0) {
                                  setSelectedPair(isSelected ? null : { author, overwriter });
                                }
                              }}
                              disabled={count === 0}
                              className={`w-full py-1.5 px-2 rounded text-xs transition-all flex flex-col items-center justify-center ${cellBg} ${
                                count > 0 ? 'cursor-pointer' : 'opacity-40 cursor-default'
                              }`}
                              title={
                                count > 0
                                  ? `${author} wrote ➔ ${overwriter} rewrote ${count} times`
                                  : 'No collisions'
                              }
                            >
                              <span>{count > 0 ? count : '—'}</span>
                              {edge && edge.lines_deleted > 0 && (
                                <span className="text-[9px] opacity-80">-{edge.lines_deleted}L</span>
                              )}
                            </button>
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {selectedPair && (
            <div className="p-2.5 rounded-lg bg-rose-500/15 border border-rose-500/30 flex items-center justify-between text-xs">
              <span className="text-rose-300 font-mono">
                Filtering by: <strong>{selectedPair.author}</strong> ➔ <strong>{selectedPair.overwriter}</strong>
              </span>
              <button
                type="button"
                onClick={() => setSelectedPair(null)}
                className="text-[10px] text-white hover:underline font-mono"
              >
                Clear Filter (Show All)
              </button>
            </div>
          )}
        </div>

        {/* Right: Live Collision Timeline Feed ("Who Broke Whose Code?") */}
        <div className="lg:col-span-6 panel p-4 bg-slate-950/90 border border-white/10 rounded-xl space-y-3 flex flex-col max-h-[600px] overflow-hidden">
          <div className="flex items-center justify-between border-b border-white/5 pb-2">
            <div className="flex items-center gap-2 text-xs font-semibold text-white">
              <Flame className="h-4 w-4 text-rose-400" />
              <span>Inter-Agent Overwrites Stream</span>
            </div>
            <span className="text-[10px] font-mono text-slate-400">
              {filteredCollisions.length} collision events
            </span>
          </div>

          <div className="divide-y divide-white/5 overflow-y-auto flex-1 space-y-2 pr-1">
            {isLoading && (
              <div className="py-8 text-center text-xs text-slate-500 animate-pulse">
                Analyzing multi-agent collision lineage…
              </div>
            )}

            {!isLoading && filteredCollisions.length === 0 && (
              <div className="py-12 text-center text-xs text-slate-500">
                No collision records matching current filter.
              </div>
            )}

            {filteredCollisions.map((c) => {
              const isExpanded = expandedEventId === c.event_id;
              const isCross = c.is_cross_agent;

              return (
                <div
                  key={c.event_id}
                  className={`p-3 rounded-xl border transition-all text-xs space-y-2 ${
                    isCross
                      ? 'bg-gradient-to-r from-rose-950/20 to-slate-900/50 border-rose-500/30'
                      : 'bg-slate-900/40 border-white/5'
                  }`}
                >
                  {/* Collision Models Flow Header */}
                  <div className="flex items-center justify-between gap-2 flex-wrap font-mono">
                    <div className="flex items-center gap-2 flex-wrap">
                      <div className="flex items-center gap-1.5 px-2 py-0.5 rounded bg-indigo-500/15 border border-indigo-500/30 text-indigo-300 font-semibold">
                        <Bot className="h-3 w-3 text-indigo-400" />
                        <span>{c.author_model}</span>
                      </div>

                      <ArrowRight className="h-3.5 w-3.5 text-rose-400 animate-pulse" />

                      <div className="flex items-center gap-1.5 px-2 py-0.5 rounded bg-rose-500/20 border border-rose-500/40 text-rose-300 font-bold">
                        <Bot className="h-3 w-3 text-rose-400" />
                        <span>{c.overwriter_model}</span>
                      </div>

                      <span
                        className={`text-[10px] px-1.5 py-0.2 rounded font-bold uppercase ${
                          c.action === 'DELETED'
                            ? 'bg-red-500/20 text-red-400 border border-red-500/40'
                            : 'bg-amber-500/20 text-amber-300 border border-amber-500/40'
                        }`}
                      >
                        {c.action}
                      </span>
                    </div>

                    <div className="text-[10px] text-slate-500 flex items-center gap-1">
                      <Clock className="h-3 w-3" />
                      <span>{c.time_delta_seconds > 0 ? fmtDeltaDuration(c.time_delta_seconds) : fmtRelativeTime(c.overwriter_time)}</span>
                    </div>
                  </div>

                  {/* Target Symbol & File */}
                  <div className="space-y-1">
                    <div className="font-mono text-xs font-semibold text-slate-200 truncate" title={c.node_signature}>
                      {c.node_signature}
                    </div>
                    <div className="flex items-center justify-between text-[11px] text-slate-400 font-mono">
                      <span className="truncate max-w-[260px] text-slate-400" title={c.file_path}>
                        {c.file_path}
                      </span>
                      <div className="flex items-center gap-2">
                        {c.added_lines > 0 && <span className="text-emerald-400">+{c.added_lines}</span>}
                        {c.deleted_lines > 0 && <span className="text-rose-400">-{c.deleted_lines}</span>}
                      </div>
                    </div>
                  </div>

                  {/* Expand Diff Trigger */}
                  {c.diff_snippet && (
                    <div className="pt-1">
                      <button
                        type="button"
                        onClick={() => setExpandedEventId(isExpanded ? null : c.event_id)}
                        className="text-[10px] text-rose-400 hover:text-rose-300 flex items-center gap-1 font-mono transition-colors"
                      >
                        {isExpanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
                        {isExpanded ? 'Hide Overwrite Diff' : 'Inspect Overwrite Diff'}
                      </button>

                      {isExpanded && (
                        <div className="mt-2">
                          <RichDiffViewer
                            diff={c.diff_snippet}
                            filePath={c.file_path}
                            signature={c.node_signature}
                            action={c.action}
                            maxHeight="200px"
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
      </div>
    </div>
  );
}
