import { useState, useMemo } from 'react';
import {
  Terminal,
  ChevronDown,
  ChevronRight,
  Copy,
  Check,
  Search,
  Zap,
  CheckCircle2,
  AlertCircle,
} from 'lucide-react';
import { useIPCTraffic } from '../hooks/useMetrics';
import type { IPCTrafficRecord } from '../types';

interface IPCTrafficViewProps {
  limit?: number;
}

function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const diff = Math.max(0, Date.now() - t);
  if (diff < 1000) return 'just now';
  if (diff < 60_000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  return `${Math.floor(diff / 3_600_000)}h ago`;
}

export function IPCTrafficView({ limit = 100 }: IPCTrafficViewProps) {
  const { data: traffic = [], isLoading } = useIPCTraffic();
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [methodFilter, setMethodFilter] = useState<string>('ALL');
  const [search, setSearch] = useState('');
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    return traffic.slice(0, limit).filter((item: IPCTrafficRecord) => {
      if (methodFilter !== 'ALL' && !item.method.toLowerCase().includes(methodFilter.toLowerCase())) {
        return false;
      }
      if (!q) return true;
      const raw = JSON.stringify(item).toLowerCase();
      return raw.includes(q);
    });
  }, [traffic, limit, methodFilter, search]);

  const handleCopy = (id: string, text: string, evt: React.MouseEvent) => {
    evt.stopPropagation();
    navigator.clipboard.writeText(text);
    setCopiedId(id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  const getMethodBadge = (method: string) => {
    if (method.includes('file_health')) {
      return 'bg-cyan-500/15 text-cyan-400 border border-cyan-500/30';
    }
    if (method.includes('report_run') || method.includes('telemetry')) {
      return 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/30';
    }
    if (method.includes('guardrail') || method.includes('lock')) {
      return 'bg-purple-500/15 text-purple-400 border border-purple-500/30';
    }
    return 'bg-slate-500/15 text-slate-400 border border-slate-500/30';
  };

  return (
    <div className="panel space-y-3">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/5 pb-3">
        <div className="flex items-center gap-2">
          <Terminal className="h-4 w-4 text-cyan-400" />
          <h2 className="font-semibold tracking-tight text-sm flex items-center gap-2">
            IPC Named Pipe Inspector
            <span className="text-[11px] font-normal text-cyan-400/80 bg-cyan-500/10 px-2 py-0.5 rounded-md border border-cyan-500/20 font-mono">
              \\.\pipe\wrongtrace
            </span>
            <span className="text-xs font-normal text-slate-500 font-mono">
              ({traffic.length} calls captured)
            </span>
          </h2>
        </div>

        <div className="flex items-center gap-2">
          {/* Method Filter */}
          <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
            {['ALL', 'file_health', 'report_run', 'ping'].map((m) => (
              <button
                key={m}
                onClick={() => setMethodFilter(m)}
                className={`px-2 py-0.5 rounded text-[10px] font-medium transition-all ${
                  methodFilter === m
                    ? 'bg-cyan-500 text-slate-950 font-bold shadow-sm'
                    : 'text-slate-400 hover:text-slate-200'
                }`}
              >
                {m}
              </button>
            ))}
          </div>

          {/* Search */}
          <div className="relative">
            <Search className="h-3 w-3 absolute left-2 top-1/2 -translate-y-1/2 text-slate-500" />
            <input
              type="text"
              placeholder="Search IPC payload…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="pl-7 pr-2.5 py-1 text-xs bg-slate-900 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-cyan-400 w-36 sm:w-48"
            />
          </div>
        </div>
      </div>

      {/* Traffic List */}
      {isLoading && traffic.length === 0 ? (
        <div className="text-center py-8 text-xs text-slate-500">
          Listening on Windows Named Pipe <code className="text-cyan-400">\\.\pipe\wrongtrace</code>…
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-8 text-xs text-slate-500 space-y-1">
          <p>No IPC interactions match your current filter.</p>
          <p className="text-[11px] text-slate-600">
            Agents like WrongStack communicate with the daemon via JSON-RPC over the pipe.
          </p>
        </div>
      ) : (
        <div className="divide-y divide-white/5 font-mono text-xs max-h-[520px] overflow-y-auto">
          {filtered.map((item: IPCTrafficRecord) => {
            const isExp = expandedId === item.id;
            const hasError = !!item.error;
            const paramPath = item.params?.file_path || item.params?.path;
            const paramRunId = item.params?.run_id;
            const paramModel = item.params?.model_name || item.params?.model;
            const paramAgent = item.params?.agent_name || item.params?.agent;

            return (
              <div key={item.id} className="transition-colors hover:bg-slate-900/60">
                {/* Row Summary */}
                <div
                  onClick={() => setExpandedId(isExp ? null : item.id)}
                  className="px-3 py-2.5 flex items-center justify-between gap-3 cursor-pointer select-none"
                >
                  <div className="flex items-center gap-2.5 min-w-0 flex-1">
                    <span className="text-slate-600 shrink-0">
                      {isExp ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                    </span>

                    {/* Method Badge */}
                    <span className={`px-2 py-0.5 rounded text-[10px] font-bold shrink-0 ${getMethodBadge(item.method)}`}>
                      {item.method}
                    </span>

                    {/* Payload Summary Info */}
                    <div className="flex items-center gap-2 truncate text-slate-300">
                      {paramPath && (
                        <span className="truncate text-slate-200 font-medium">
                          {String(paramPath)}
                        </span>
                      )}
                      {paramRunId && (
                        <span className="text-slate-400 text-[11px]">
                          run:<span className="text-cyan-300">{String(paramRunId)}</span>
                        </span>
                      )}
                      {paramAgent && (
                        <span className="text-emerald-400 text-[11px] bg-emerald-500/10 px-1.5 py-0.2 rounded border border-emerald-500/20">
                          {String(paramAgent)}
                        </span>
                      )}
                      {paramModel && (
                        <span className="text-purple-400 text-[11px] bg-purple-500/10 px-1.5 py-0.2 rounded border border-purple-500/20">
                          {String(paramModel)}
                        </span>
                      )}
                    </div>
                  </div>

                  {/* Latency & Status */}
                  <div className="flex items-center gap-3 shrink-0 text-[11px]">
                    <span className="text-emerald-400/90 font-semibold flex items-center gap-1 bg-emerald-500/10 px-1.5 py-0.5 rounded border border-emerald-500/20">
                      <Zap className="h-3 w-3" />
                      {item.duration_ms.toFixed(2)}ms
                    </span>

                    {hasError ? (
                      <span className="flex items-center gap-1 text-red-400 font-semibold bg-red-500/10 px-1.5 py-0.5 rounded border border-red-500/20">
                        <AlertCircle className="h-3 w-3" />
                        RPC Error
                      </span>
                    ) : (
                      <span className="flex items-center gap-1 text-emerald-400/80 bg-emerald-500/10 px-1.5 py-0.5 rounded border border-emerald-500/20">
                        <CheckCircle2 className="h-3 w-3" />
                        OK
                      </span>
                    )}

                    <span className="text-slate-500 text-[10px]" title={item.timestamp}>
                      {relativeTime(item.timestamp)}
                    </span>
                  </div>
                </div>

                {/* Expanded JSON Inspector */}
                {isExp && (
                  <div className="px-4 pb-3 pt-1 border-t border-white/5 bg-slate-950/80 space-y-2">
                    <div className="flex items-center justify-between text-[11px] text-slate-400 pt-1">
                      <span className="font-semibold text-slate-300 flex items-center gap-1.5">
                        <Terminal className="h-3 w-3 text-cyan-400" />
                        JSON-RPC Request & Response Details
                      </span>
                      <button
                        onClick={(e) => handleCopy(item.id, JSON.stringify(item, null, 2), e)}
                        className="inline-flex items-center gap-1 text-xs text-slate-400 hover:text-slate-200 bg-slate-900 border border-white/10 px-2 py-0.5 rounded"
                      >
                        {copiedId === item.id ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                        Copy Full JSON
                      </button>
                    </div>

                    <div className="grid grid-cols-1 md:grid-cols-2 gap-3 pt-1">
                      {/* Request Params */}
                      <div className="bg-slate-900/90 rounded-lg p-2.5 border border-white/10 space-y-1">
                        <div className="text-[10px] uppercase tracking-wider font-bold text-slate-400">
                          Incoming Request Params
                        </div>
                        <pre className="text-[11px] text-cyan-300 font-mono overflow-x-auto whitespace-pre-wrap max-h-48">
                          {JSON.stringify(item.params || {}, null, 2)}
                        </pre>
                      </div>

                      {/* Response Result */}
                      <div className="bg-slate-900/90 rounded-lg p-2.5 border border-white/10 space-y-1">
                        <div className="text-[10px] uppercase tracking-wider font-bold text-slate-400">
                          {hasError ? 'RPC Error Output' : 'Daemon Result Returned'}
                        </div>
                        <pre className={`text-[11px] font-mono overflow-x-auto whitespace-pre-wrap max-h-48 ${hasError ? 'text-red-400' : 'text-emerald-300'}`}>
                          {JSON.stringify(item.error || item.result || {}, null, 2)}
                        </pre>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
