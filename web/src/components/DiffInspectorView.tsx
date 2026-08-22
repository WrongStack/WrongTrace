import { useState, useMemo } from 'react';
import {
  Code2,
  FileCode,
  Search,
  Filter,
  Copy,
  Check,
  Plus,
  Pencil,
  Trash2,
  Layers,
  ArrowUpDown,
  Sparkles,
  Download,
  Calendar,
  Bot,
} from 'lucide-react';
import { RichDiffViewer } from './RichDiffViewer';
import type { EventRecord } from '../types';

interface DiffInspectorViewProps {
  events: EventRecord[];
  loading: boolean;
}

export function DiffInspectorView({ events, loading }: DiffInspectorViewProps) {
  const [selectedEventId, setSelectedEventId] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [selectedAction, setSelectedAction] = useState<string>('ALL');
  const [selectedFile, setSelectedFile] = useState<string>('ALL');
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [viewFormat, setViewFormat] = useState<'unified' | 'raw'>('unified');

  // Unique files for dropdown filter
  const uniqueFiles = useMemo(() => {
    const set = new Set<string>();
    events.forEach((e) => {
      if (e.file_path) set.add(e.file_path);
    });
    return Array.from(set).sort();
  }, [events]);

  const filteredEvents = useMemo(() => {
    const q = search.toLowerCase().trim();
    return events.filter((e) => {
      if (selectedAction !== 'ALL' && e.action !== selectedAction) return false;
      if (selectedFile !== 'ALL' && e.file_path !== selectedFile) return false;
      if (q && !e.file_path.toLowerCase().includes(q) && !e.node_signature.toLowerCase().includes(q)) {
        return false;
      }
      return true;
    });
  }, [events, search, selectedAction, selectedFile]);

  // Selected event for detail pane
  const currentEvent = useMemo(() => {
    if (selectedEventId) {
      const found = filteredEvents.find((e) => e.event_id === selectedEventId);
      if (found) return found;
    }
    return filteredEvents[0] ?? null;
  }, [selectedEventId, filteredEvents]);

  const handleCopy = (id: string, text: string) => {
    navigator.clipboard.writeText(text);
    setCopiedId(id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  const handleExportJSON = () => {
    const dataStr = 'data:text/json;charset=utf-8,' + encodeURIComponent(JSON.stringify(filteredEvents, null, 2));
    const dlAnchorElem = document.createElement('a');
    dlAnchorElem.setAttribute('href', dataStr);
    dlAnchorElem.setAttribute('download', `wrongtrace-diffs-${new Date().toISOString().slice(0, 10)}.json`);
    dlAnchorElem.click();
  };

  return (
    <div className="space-y-4">
      {/* Top Header & Toolbar */}
      <div className="panel flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-lg bg-accent/20 text-accent">
            <Code2 className="h-5 w-5" />
          </div>
          <div>
            <h2 className="font-semibold tracking-tight text-base flex items-center gap-2">
              Code & Line Diff Inspector
              <span className="text-xs font-normal text-slate-400">
                · {filteredEvents.length} transition records
              </span>
            </h2>
            <p className="text-xs text-slate-400">
              Line-by-line unified diffs, added/deleted line counters, and semantic revision history.
            </p>
          </div>
        </div>

        {/* Filter Controls */}
        <div className="flex flex-wrap items-center gap-2.5">
          {/* Search */}
          <div className="relative">
            <Search className="h-3.5 w-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
            <input
              type="text"
              placeholder="Search file, symbol…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="pl-8 pr-3 py-1.5 text-xs bg-slate-900 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent w-44 sm:w-56"
            />
          </div>

          {/* Action Filter */}
          <select
            value={selectedAction}
            onChange={(e) => setSelectedAction(e.target.value)}
            className="text-xs bg-slate-900 border border-white/10 rounded-lg px-2.5 py-1.5 text-slate-300 focus:outline-none focus:border-accent"
          >
            <option value="ALL">All Actions</option>
            <option value="MODIFIED">Modified Only</option>
            <option value="ADDED">Added Only</option>
            <option value="DELETED">Deleted Only</option>
          </select>

          {/* File Filter */}
          <select
            value={selectedFile}
            onChange={(e) => setSelectedFile(e.target.value)}
            className="text-xs bg-slate-900 border border-white/10 rounded-lg px-2.5 py-1.5 text-slate-300 focus:outline-none focus:border-accent max-w-[160px] truncate"
          >
            <option value="ALL">All Files ({uniqueFiles.length})</option>
            {uniqueFiles.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>

          {/* Export */}
          <button
            onClick={handleExportJSON}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs bg-slate-900 hover:bg-slate-800 border border-white/10 rounded-lg text-slate-300 transition-all"
            title="Export Diffs to JSON"
          >
            <Download className="h-3.5 w-3.5" />
            Export
          </button>
        </div>
      </div>

      {/* Summary KPI Strip */}
      {filteredEvents.length > 0 && (
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div className="panel-raised p-2.5 font-mono text-xs">
            <div className="text-[10px] text-slate-400">TOTAL INSERTIONS</div>
            <div className="text-emerald-400 font-bold text-sm mt-0.5">
              +{filteredEvents.reduce((acc, e) => acc + (e.added_lines ?? (e.action === 'ADDED' ? e.lines_of_code || 10 : 0)), 0).toLocaleString()} lines
            </div>
          </div>
          <div className="panel-raised p-2.5 font-mono text-xs">
            <div className="text-[10px] text-slate-400">TOTAL DELETIONS</div>
            <div className="text-rose-400 font-bold text-sm mt-0.5">
              -{filteredEvents.reduce((acc, e) => acc + (e.deleted_lines ?? (e.action === 'DELETED' ? e.lines_of_code || 10 : 0)), 0).toLocaleString()} lines
            </div>
          </div>
          <div className="panel-raised p-2.5 font-mono text-xs">
            <div className="text-[10px] text-slate-400">UNIQUE FILES CHURNED</div>
            <div className="text-cyan-300 font-bold text-sm mt-0.5">
              {uniqueFiles.length} files
            </div>
          </div>
          <div className="panel-raised p-2.5 font-mono text-xs">
            <div className="text-[10px] text-slate-400">AST MUTATION RATE</div>
            <div className="text-amber-400 font-bold text-sm mt-0.5">
              {filteredEvents.filter((e) => e.action === 'MODIFIED').length} modifications
            </div>
          </div>
        </div>
      )}

      {/* Main Split Layout: Left List, Right Code Diff View */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-4">
        {/* Left: Events List */}
        <div className="lg:col-span-5 panel p-0 overflow-hidden flex flex-col h-[680px]">
          <div className="p-3 bg-white/5 border-b border-white/5 flex items-center justify-between text-xs text-slate-400 font-medium">
            <span>Transitions Timeline</span>
            <span>{filteredEvents.length} recorded</span>
          </div>

          <div className="divide-y divide-white/5 overflow-y-auto flex-1 p-2 space-y-1">
            {loading && <div className="p-4 text-xs text-slate-500">Loading diff records…</div>}

            {!loading && filteredEvents.length === 0 && (
              <div className="p-8 text-center text-xs text-slate-500">
                No code diff records match the criteria.
              </div>
            )}

            {filteredEvents.map((e) => {
              const isSelected = currentEvent?.event_id === e.event_id;
              const added = e.added_lines ?? 0;
              const deleted = e.deleted_lines ?? 0;
              const start = e.start_line ?? 0;
              const end = e.end_line ?? 0;

              return (
                <div
                  key={e.event_id}
                  onClick={() => setSelectedEventId(e.event_id)}
                  className={`p-3 rounded-lg border transition-all cursor-pointer text-xs space-y-1.5 ${
                    isSelected
                      ? 'bg-accent/15 border-accent/40 shadow-sm'
                      : 'bg-slate-900/50 border-transparent hover:border-white/10 hover:bg-white/[0.03]'
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span
                      className={`chip text-[10px] px-1.5 py-0.2 rounded font-mono font-semibold uppercase ${
                        e.action === 'ADDED'
                          ? 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30'
                          : e.action === 'MODIFIED'
                          ? 'bg-amber-500/15 text-amber-400 border border-amber-500/30'
                          : 'bg-red-500/15 text-red-400 border border-red-500/30'
                      }`}
                    >
                      {e.action}
                    </span>

                    <span className="text-[10px] text-slate-500 font-mono">
                      {new Date(e.event_time).toLocaleTimeString()}
                    </span>
                  </div>

                  <div className="font-mono text-xs font-semibold text-slate-200 truncate" title={e.node_signature}>
                    {e.node_signature}
                  </div>

                  <div className="flex items-center justify-between text-[11px] text-slate-400">
                    <span className="font-mono truncate max-w-[200px] text-slate-400" title={e.file_path}>
                      {e.file_path}
                    </span>
                    <div className="flex items-center gap-2 font-mono text-[10px]">
                      {start > 0 && <span className="text-slate-500">L{start}-L{end}</span>}
                      {(added > 0 || deleted > 0) && (
                        <span>
                          {added > 0 && <span className="text-emerald-400">+{added} </span>}
                          {deleted > 0 && <span className="text-red-400">-{deleted}</span>}
                        </span>
                      )}
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>

        {/* Right: Full Diff Inspector Canvas */}
        <div className="lg:col-span-7 panel p-0 overflow-hidden flex flex-col h-[680px] bg-[#0c1017]">
          {currentEvent ? (
            <>
              {/* Diff Header */}
              <div className="p-4 bg-slate-900/90 border-b border-white/10 flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-0 space-y-1">
                  <div className="flex items-center gap-2">
                    <span
                      className={`chip text-[10px] px-2 py-0.5 rounded font-mono font-semibold uppercase ${
                        currentEvent.action === 'ADDED'
                          ? 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30'
                          : currentEvent.action === 'MODIFIED'
                          ? 'bg-amber-500/15 text-amber-400 border border-amber-500/30'
                          : 'bg-red-500/15 text-red-400 border border-red-500/30'
                      }`}
                    >
                      {currentEvent.action}
                    </span>
                    <h3 className="font-mono text-sm font-semibold text-white truncate max-w-[360px]" title={currentEvent.node_signature}>
                      {currentEvent.node_signature}
                    </h3>
                  </div>

                  <div className="flex flex-wrap items-center gap-3 text-xs text-slate-400 font-mono">
                    <span className="flex items-center gap-1 text-slate-300">
                      <FileCode className="h-3.5 w-3.5 text-cyan-400" />
                      {currentEvent.file_path}
                    </span>
                    {currentEvent.start_line ? (
                      <span>· Lines {currentEvent.start_line} - {currentEvent.end_line}</span>
                    ) : null}
                    {currentEvent.run_id && (
                      <span className="flex items-center gap-1 text-[10px] px-2 py-0.5 rounded bg-indigo-500/10 text-indigo-300 border border-indigo-500/20 font-mono">
                        <Bot className="h-3 w-3 text-indigo-400" />
                        Run: {currentEvent.run_id}
                      </span>
                    )}
                  </div>
                </div>
              </div>

              {/* Rich Diff Body Content */}
              <div className="p-3 flex-1 overflow-auto">
                <RichDiffViewer
                  diff={currentEvent.diff_snippet}
                  filePath={currentEvent.file_path}
                  signature={currentEvent.node_signature}
                  action={currentEvent.action}
                  startLine={currentEvent.start_line}
                  endLine={currentEvent.end_line}
                  maxHeight="520px"
                />
              </div>

              {/* Metadata Footer */}
              <div className="px-4 py-2.5 bg-slate-950 border-t border-white/5 flex items-center justify-between text-[11px] text-slate-400 font-mono">
                <div className="flex items-center gap-4">
                  <span>Lines of Code: {currentEvent.lines_of_code ?? 0}</span>
                  {currentEvent.ast_content_hash && (
                    <span className="truncate max-w-[240px]">Hash: {currentEvent.ast_content_hash}</span>
                  )}
                </div>
                <div>Recorded: {new Date(currentEvent.event_time).toLocaleString()}</div>
              </div>
            </>
          ) : (
            <div className="flex flex-col items-center justify-center h-full text-slate-500 text-sm gap-2">
              <Code2 className="h-8 w-8 text-slate-600" />
              <span>Select an event from the timeline to inspect its code diff.</span>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
