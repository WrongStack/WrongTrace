import { useMemo, useState, useEffect, useRef } from 'react';
import {
  History,
  GitCommitHorizontal,
  Plus,
  Minus,
  Bot,
  Clock,
  ArrowDownNarrowWide,
  ArrowUpNarrowWide,
  MoveVertical,
  MoveHorizontal,
  FileCode,
  ChevronDown,
  ChevronRight,
  CheckCircle2,
  Layers,
  Flame,
} from 'lucide-react';
import { useRecentFileEvents } from '../hooks/useMetrics';
import { RichDiffViewer } from './RichDiffViewer';
import type { EventRecord } from '../types';

// A "revision" collapses bursts of AST mutation events that happened within
// REVISION_BURST_MS of each other into one commit-like node, so the timeline
// reads like a git history instead of hundreds of raw symbol rows.
const REVISION_BURST_MS = 120_000;
const INITIAL_VISIBLE_REVISIONS = 40;

interface FileRevision {
  key: string;
  startTime: string;
  endTime: string;
  added: number;
  deleted: number;
  events: EventRecord[];
  models: string[];
  hasAdded: boolean;
  hasModified: boolean;
  hasDeleted: boolean;
  symbolsTouched: number;
  endLoc: number | null;
}

function groupIntoRevisions(events: EventRecord[]): FileRevision[] {
  const chrono = [...events].sort(
    (a, b) => a.event_time.localeCompare(b.event_time) || a.event_id.localeCompare(b.event_id),
  );
  const revisions: FileRevision[] = [];

  for (const ev of chrono) {
    const t = Date.parse(ev.event_time);
    const last = revisions[revisions.length - 1];
    const lastT = last ? Date.parse(last.endTime) : Number.NaN;

    if (last && Number.isFinite(t) && Number.isFinite(lastT) && t - lastT <= REVISION_BURST_MS) {
      last.events.push(ev);
      last.endTime = ev.event_time;
      last.added += ev.added_lines || 0;
      last.deleted += ev.deleted_lines || 0;
      if (ev.author_model && !last.models.includes(ev.author_model)) last.models.push(ev.author_model);
      if (ev.action === 'ADDED') last.hasAdded = true;
      else if (ev.action === 'DELETED') last.hasDeleted = true;
      else last.hasModified = true;
      if (ev.node_signature) last.symbolsTouched += 1;
      if (ev.lines_of_code != null) last.endLoc = ev.lines_of_code;
    } else {
      revisions.push({
        key: ev.event_id,
        startTime: ev.event_time,
        endTime: ev.event_time,
        added: ev.added_lines || 0,
        deleted: ev.deleted_lines || 0,
        events: [ev],
        models: ev.author_model ? [ev.author_model] : [],
        hasAdded: ev.action === 'ADDED',
        hasModified: ev.action !== 'ADDED' && ev.action !== 'DELETED',
        hasDeleted: ev.action === 'DELETED',
        symbolsTouched: ev.node_signature ? 1 : 0,
        endLoc: ev.lines_of_code ?? null,
      });
    }
  }

  return revisions;
}

function fmtRelativeTime(isoStr: string): string {
  const t = Date.parse(isoStr);
  if (Number.isNaN(t)) return isoStr;
  const diffSec = Math.round((Date.now() - t) / 1000);
  if (diffSec < 60) return `${Math.max(0, diffSec)}s ago`;
  const diffMin = Math.round(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHour = Math.round(diffMin / 60);
  if (diffHour < 24) return `${diffHour}h ago`;
  const diffDay = Math.round(diffHour / 24);
  return `${diffDay}d ago`;
}

function fmtAbsoluteTime(isoStr: string): string {
  const t = Date.parse(isoStr);
  if (Number.isNaN(t)) return isoStr;
  return new Date(t).toLocaleString();
}

function fmtCompactTime(isoStr: string): string {
  const t = Date.parse(isoStr);
  if (Number.isNaN(t)) return isoStr;
  const d = new Date(t);
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return sameDay ? time : `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${time}`;
}

interface RevisionBadgeProps {
  rev: FileRevision;
}

function RevisionActionBadges({ rev }: RevisionBadgeProps) {
  return (
    <span className="flex items-center gap-1">
      {rev.hasAdded && (
        <span className="text-[9px] font-mono font-bold px-1 py-0.2 rounded bg-emerald-500/20 text-emerald-400 border border-emerald-500/30">
          ADD
        </span>
      )}
      {rev.hasModified && (
        <span className="text-[9px] font-mono font-bold px-1 py-0.2 rounded bg-amber-500/20 text-amber-400 border border-amber-500/30">
          MOD
        </span>
      )}
      {rev.hasDeleted && (
        <span className="text-[9px] font-mono font-bold px-1 py-0.2 rounded bg-rose-500/20 text-rose-400 border border-rose-500/30">
          DEL
        </span>
      )}
    </span>
  );
}

interface RevisionDetailProps {
  rev: FileRevision;
}

function RevisionDetail({ rev }: RevisionDetailProps) {
  return (
    <div className="mt-2.5 space-y-2 text-xs border-t border-white/5 pt-2">
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-1.5 text-[11px] font-mono">
        <div className="panel-raised p-1.5">
          <div className="text-slate-500 text-[10px]">Delta</div>
          <div className="flex items-center gap-1.5">
            <span className="text-emerald-400">+{rev.added}</span>
            <span className="text-rose-400">-{rev.deleted}</span>
          </div>
        </div>
        <div className="panel-raised p-1.5">
          <div className="text-slate-500 text-[10px]">Symbols</div>
          <div className="text-slate-200 font-semibold">{rev.symbolsTouched} mutated</div>
        </div>
        <div className="panel-raised p-1.5">
          <div className="text-slate-500 text-[10px]">Events</div>
          <div className="text-slate-200 font-semibold">{rev.events.length}</div>
        </div>
        <div className="panel-raised p-1.5">
          <div className="text-slate-500 text-[10px]">LOC after</div>
          <div className="text-cyan-300 font-semibold">{rev.endLoc ?? '—'}</div>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-[10px] text-slate-500 font-mono flex items-center gap-1">
          <Clock className="h-3 w-3" /> {fmtAbsoluteTime(rev.startTime)}
        </span>
        {rev.models.map((m) => (
          <span
            key={m}
            className="text-[10px] font-mono px-1.5 py-0.2 rounded bg-cyan-500/10 text-cyan-300 border border-cyan-500/20 flex items-center gap-1"
          >
            <Bot className="h-2.5 w-2.5" />
            {m}
          </span>
        ))}
      </div>

      {rev.events
        .filter((ev) => ev.diff_snippet)
        .slice(0, 6)
        .map((ev) => (
          <div key={ev.event_id}>
            <div className="text-[10px] text-slate-400 mb-1 font-mono flex items-center justify-between">
              <span className="truncate max-w-[60%]" title={ev.node_signature}>
                {ev.node_signature || ev.file_path}
              </span>
              <span className="text-slate-500">
                {ev.action} · L{ev.start_line ?? '?'}-L{ev.end_line ?? '?'}
              </span>
            </div>
            <RichDiffViewer
              diff={ev.diff_snippet}
              filePath={ev.file_path}
              signature={ev.node_signature}
              action={ev.action}
              startLine={ev.start_line}
              endLine={ev.end_line}
              maxHeight="140px"
            />
          </div>
        ))}
    </div>
  );
}

interface FileHistoryTimelineProps {
  filePath: string;
}

// Chronological commit-graph of every recorded mutation of one file.
// Renders vertically (default, git-log style newest on top) or horizontally;
// the +/- sparkline strip on top gives the whole lifetime at a glance.
export function FileHistoryTimeline({ filePath }: FileHistoryTimelineProps) {
  const { data: events = [], isLoading } = useRecentFileEvents(filePath, 1000);
  const [orientation, setOrientation] = useState<'vertical' | 'horizontal'>('vertical');
  const [newestFirst, setNewestFirst] = useState(true);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const [visibleCount, setVisibleCount] = useState(INITIAL_VISIBLE_REVISIONS);
  const listRef = useRef<HTMLDivElement>(null);

  const revisions = useMemo(() => groupIntoRevisions(events), [events]);

  const totals = useMemo(() => {
    let added = 0;
    let deleted = 0;
    const models = new Set<string>();
    for (const rev of revisions) {
      added += rev.added;
      deleted += rev.deleted;
      rev.models.forEach((m) => models.add(m));
    }
    return { added, deleted, models: models.size };
  }, [revisions]);

  const displayed = useMemo(() => {
    const capped = revisions.slice(0, visibleCount);
    return newestFirst ? [...capped].reverse() : capped;
  }, [revisions, visibleCount, newestFirst]);

  useEffect(() => {
    setExpandedKey(null);
    setVisibleCount(INITIAL_VISIBLE_REVISIONS);
  }, [filePath]);

  const maxDelta = useMemo(
    () => Math.max(1, ...revisions.map((r) => Math.max(r.added, r.deleted))),
    [revisions],
  );

  const focusRevision = (key: string) => {
    setExpandedKey(key);
    const el = document.getElementById(`fh-rev-${filePath}-${key}`);
    el?.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'center' });
  };

  if (isLoading) {
    return (
      <div className="p-6 rounded-xl bg-slate-950/60 border border-white/5 flex items-center justify-center text-xs text-slate-400 gap-2">
        <History className="h-4 w-4 animate-spin text-cyan-400" />
        <span>Loading file lifetime history…</span>
      </div>
    );
  }

  if (revisions.length === 0) {
    return (
      <div className="p-4 rounded-xl bg-slate-950/40 border border-white/5 text-xs text-slate-400 flex items-center gap-2">
        <GitCommitHorizontal className="h-4 w-4 text-slate-500" />
        No mutation events recorded for this file yet. Changes appear here as AI agents edit it.
      </div>
    );
  }

  const firstTime = revisions[0]?.startTime;
  const lastTime = revisions[revisions.length - 1]?.endTime;
  const isBursty = revisions.length > 0 && revisions.some((r) => r.symbolsTouched >= 5);

  return (
    <div className="space-y-3 p-3.5 rounded-xl bg-slate-950/80 border border-cyan-500/20 shadow-inner">
      {/* Header stats */}
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-white/10 pb-2.5">
        <div className="flex items-center gap-2">
          <div className="p-1 rounded-lg bg-cyan-500/20 text-cyan-400">
            <GitCommitHorizontal className="h-4 w-4" />
          </div>
          <div>
            <h4 className="font-semibold text-xs text-white">File Lifetime Commit Graph</h4>
            <div className="text-[10px] text-slate-400 font-mono">
              {fmtRelativeTime(firstTime)} → {fmtRelativeTime(lastTime)} · {revisions.length} revisions
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2 text-[11px] font-mono">
          <span className="text-emerald-400 flex items-center gap-0.5">
            <Plus className="h-3 w-3" />
            {totals.added.toLocaleString()}
          </span>
          <span className="text-rose-400 flex items-center gap-0.5">
            <Minus className="h-3 w-3" />
            {totals.deleted.toLocaleString()}
          </span>
          <span className="text-slate-400 flex items-center gap-0.5">
            <Bot className="h-3 w-3" />
            {totals.models}
          </span>
          {isBursty && (
            <span className="text-amber-400 flex items-center gap-0.5" title="Some revisions touched many symbols — possible thrash bursts">
              <Flame className="h-3 w-3" />
            </span>
          )}
        </div>
      </div>

      {/* Controls */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-[10px] text-slate-500 font-mono">Click a bar or node to inspect that revision</span>
        <div className="flex items-center gap-1.5">
          <div className="flex items-center bg-slate-950 border border-white/10 rounded-md p-0.5 text-[10px]">
            <button
              type="button"
              onClick={() => setOrientation('vertical')}
              className={`flex items-center gap-1 px-1.5 py-0.5 rounded transition-all ${
                orientation === 'vertical' ? 'bg-accent text-white font-semibold' : 'text-slate-400 hover:text-white'
              }`}
              title="Vertical timeline (newest on top)"
            >
              <MoveVertical className="h-3 w-3" />
              Vertical
            </button>
            <button
              type="button"
              onClick={() => setOrientation('horizontal')}
              className={`flex items-center gap-1 px-1.5 py-0.5 rounded transition-all ${
                orientation === 'horizontal' ? 'bg-accent text-white font-semibold' : 'text-slate-400 hover:text-white'
              }`}
              title="Horizontal timeline (newest on the right)"
            >
              <MoveHorizontal className="h-3 w-3" />
              Horizontal
            </button>
          </div>
          <button
            type="button"
            onClick={() => setNewestFirst((v) => !v)}
            className="flex items-center gap-1 px-1.5 py-0.5 rounded-md bg-slate-950 border border-white/10 text-[10px] text-slate-400 hover:text-white transition-all"
            title={newestFirst ? 'Showing newest first — click for oldest first' : 'Showing oldest first — click for newest first'}
          >
            {newestFirst ? (
              <>
                <ArrowDownNarrowWide className="h-3 w-3" /> Newest first
              </>
            ) : (
              <>
                <ArrowUpNarrowWide className="h-3 w-3" /> Oldest first
              </>
            )}
          </button>
        </div>
      </div>

      {/* Lifetime +/- sparkline strip (always chronological, left = oldest) */}
      <div className="rounded-lg border border-white/5 bg-slate-950/80 p-2 overflow-x-auto">
        <div className="flex items-end justify-between gap-px h-14 min-w-full">
          {revisions.map((rev, i) => (
            <button
              key={`bar-${rev.key}`}
              type="button"
              onClick={() => focusRevision(rev.key)}
              title={`r${i + 1} · ${fmtAbsoluteTime(rev.startTime)} · +${rev.added} -${rev.deleted} · ${rev.events.length} event(s)`}
              className="flex-1 min-w-[3px] flex flex-col justify-end gap-px group"
            >
              <span
                className="w-full rounded-t-sm bg-emerald-500/70 group-hover:bg-emerald-400 transition-all"
                style={{ height: `${Math.max(2, (rev.added / maxDelta) * 26)}px` }}
              />
              <span
                className="w-full rounded-b-sm bg-rose-500/70 group-hover:bg-rose-400 transition-all"
                style={{ height: `${Math.max(2, (rev.deleted / maxDelta) * 26)}px` }}
              />
            </button>
          ))}
        </div>
        <div className="flex items-center justify-between text-[9px] text-slate-600 font-mono mt-1">
          <span>{fmtCompactTime(firstTime)}</span>
          <span>{fmtCompactTime(lastTime)}</span>
        </div>
      </div>

      {/* Timeline body */}
      <div ref={listRef}>
        {orientation === 'vertical' ? (
          <div className="space-y-2 max-h-[420px] overflow-y-auto pr-1 relative before:absolute before:left-3 before:top-2 before:bottom-2 before:w-0.5 before:bg-gradient-to-b before:from-cyan-500/50 before:via-indigo-500/30 before:to-slate-800">
            {displayed.map((rev) => {
              const isExpanded = expandedKey === rev.key;
              const isLatest = rev.key === revisions[revisions.length - 1].key;
              return (
                <div
                  key={rev.key}
                  id={`fh-rev-${filePath}-${rev.key}`}
                  className={`relative pl-7 rounded-lg p-2 transition-all ${
                    isLatest
                      ? 'bg-cyan-950/20 border border-cyan-500/30 shadow-md'
                      : 'bg-slate-900/40 border border-white/5'
                  }`}
                >
                  <div
                    className={`absolute left-1.5 top-3.5 w-3 h-3 rounded-full border-2 transform -translate-x-1/2 ${
                      isLatest
                        ? 'bg-cyan-500 border-cyan-200 ring-2 ring-cyan-500/40'
                        : 'bg-slate-900 border-indigo-400'
                    }`}
                  />
                  <div
                    onClick={() => setExpandedKey(isExpanded ? null : rev.key)}
                    className="flex items-center justify-between cursor-pointer group gap-2"
                  >
                    <div className="flex items-center gap-1.5 flex-wrap min-w-0">
                      <RevisionActionBadges rev={rev} />
                      <span className="text-[11px] font-mono text-slate-300 flex items-center gap-1">
                        <Bot className="h-3 w-3 text-cyan-400" />
                        {rev.models.length > 0 ? rev.models[0] : 'unknown'}
                        {rev.models.length > 1 && <span className="text-slate-500">+{rev.models.length - 1}</span>}
                      </span>
                      {rev.symbolsTouched > 0 && (
                        <span className="text-[10px] font-mono text-purple-300 bg-purple-500/10 px-1 rounded border border-purple-500/20 flex items-center gap-0.5">
                          <Layers className="h-2.5 w-2.5" />
                          {rev.symbolsTouched} sym
                        </span>
                      )}
                      {isLatest && (
                        <span className="text-[10px] font-mono px-1 rounded bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 flex items-center gap-1">
                          <CheckCircle2 className="h-2.5 w-2.5" /> Current
                        </span>
                      )}
                    </div>
                    <div className="flex items-center gap-1.5 text-slate-400 text-[11px] font-mono shrink-0">
                      <span className="text-emerald-400">+{rev.added}</span>
                      <span className="text-rose-400">-{rev.deleted}</span>
                      <span className="hidden sm:inline">{fmtRelativeTime(rev.endTime)}</span>
                      {isExpanded ? (
                        <ChevronDown className="h-3.5 w-3.5" />
                      ) : (
                        <ChevronRight className="h-3.5 w-3.5 group-hover:text-white" />
                      )}
                    </div>
                  </div>
                  {isExpanded && <RevisionDetail rev={rev} />}
                </div>
              );
            })}
            {revisions.length > visibleCount && (
              <button
                type="button"
                onClick={() => setVisibleCount((c) => c + INITIAL_VISIBLE_REVISIONS)}
                className="w-full py-1.5 text-[11px] font-mono text-slate-400 hover:text-white rounded-lg border border-dashed border-white/10 hover:border-white/25 transition-all"
              >
                Show {Math.min(INITIAL_VISIBLE_REVISIONS, revisions.length - visibleCount)} older revisions (
                {revisions.length - visibleCount} hidden)
              </button>
            )}
          </div>
        ) : (
          <div className="space-y-2">
            <div className="overflow-x-auto pb-2">
              <div className="relative flex items-stretch gap-0 pt-5 pb-1 min-w-max">
                <div className="absolute left-0 right-0 top-[26px] h-0.5 bg-gradient-to-r from-slate-800 via-indigo-500/30 to-cyan-500/50" />
                {displayed.map((rev) => {
                  const isSelected = expandedKey === rev.key;
                  const isLatest = rev.key === revisions[revisions.length - 1].key;
                  return (
                    <div key={rev.key} className="relative flex flex-col items-center px-1">
                      <div
                        className={`w-3 h-3 rounded-full border-2 mb-2 relative z-10 ${
                          isLatest
                            ? 'bg-cyan-500 border-cyan-200 ring-2 ring-cyan-500/40'
                            : isSelected
                            ? 'bg-indigo-400 border-white'
                            : 'bg-slate-900 border-indigo-400'
                        }`}
                      />
                      <button
                        type="button"
                        id={`fh-rev-${filePath}-${rev.key}`}
                        onClick={() => setExpandedKey(isSelected ? null : rev.key)}
                        className={`w-40 rounded-lg p-1.5 text-left transition-all border ${
                          isSelected
                            ? 'bg-indigo-950/60 border-indigo-400 shadow-md'
                            : isLatest
                            ? 'bg-cyan-950/20 border-cyan-500/30 hover:border-cyan-400'
                            : 'bg-slate-900/40 border-white/5 hover:border-indigo-500/40'
                        }`}
                      >
                        <div className="flex items-center justify-between gap-1">
                          <RevisionActionBadges rev={rev} />
                          <span className="text-[9px] font-mono text-slate-500">{fmtCompactTime(rev.endTime)}</span>
                        </div>
                        <div className="mt-1 flex items-center justify-between text-[10px] font-mono">
                          <span className="text-emerald-400">+{rev.added}</span>
                          <span className="text-rose-400">-{rev.deleted}</span>
                          <span className="text-slate-500 flex items-center gap-0.5">
                            <Layers className="h-2.5 w-2.5" />
                            {rev.symbolsTouched}
                          </span>
                        </div>
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
            {expandedKey && (
              <div className="rounded-lg bg-slate-900/60 border border-indigo-500/30 p-2.5">
                {(() => {
                  const rev = revisions.find((r) => r.key === expandedKey);
                  if (!rev) return null;
                  return (
                    <>
                      <div className="flex items-center justify-between mb-1.5 text-[11px] font-mono text-slate-400">
                        <span className="flex items-center gap-1.5">
                          <FileCode className="h-3 w-3 text-cyan-400" />
                          {fmtAbsoluteTime(rev.startTime)}
                        </span>
                        <button
                          type="button"
                          onClick={() => setExpandedKey(null)}
                          className="text-slate-500 hover:text-white"
                        >
                          close
                        </button>
                      </div>
                      <RevisionDetail rev={rev} />
                    </>
                  );
                })()}
              </div>
            )}
            {revisions.length > visibleCount && (
              <button
                type="button"
                onClick={() => setVisibleCount((c) => c + INITIAL_VISIBLE_REVISIONS)}
                className="w-full py-1.5 text-[11px] font-mono text-slate-400 hover:text-white rounded-lg border border-dashed border-white/10 hover:border-white/25 transition-all"
              >
                Show {Math.min(INITIAL_VISIBLE_REVISIONS, revisions.length - visibleCount)} older revisions (
                {revisions.length - visibleCount} hidden)
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
