import React, { useState, useMemo } from 'react';
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  Clock,
  Cpu,
  Database,
  Flame,
  HardDrive,
  RefreshCw,
  Zap,
  TrendingUp,
  BarChart3,
} from 'lucide-react';
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  BarChart,
  Bar,
  CartesianGrid,
  XAxis,
  YAxis,
  Tooltip,
  Legend,
} from 'recharts';
import { useProfilerHotspots, useProfilerOverview, useProfilerTraces } from '../hooks/useMetrics';
import type { RuntimeTrace } from '../types';

export function ProfilerTracesView() {
  const tracesQuery = useProfilerTraces(100);
  const hotspotsQuery = useProfilerHotspots(20);
  const overviewQuery = useProfilerOverview();

  const [selectedTrace, setSelectedTrace] = useState<RuntimeTrace | null>(null);
  const [filterType, setFilterType] = useState<string>('all');
  const [chartView, setChartView] = useState<'latency' | 'services'>('latency');

  const traces = tracesQuery.data ?? [];
  const hotspots = hotspotsQuery.data ?? [];
  const overview = overviewQuery.data;

  const filteredTraces = useMemo(() => {
    return traces.filter((t) => {
      if (filterType === 'all') return true;
      if (filterType === 'errors') return t.status_code >= 400 || !!t.error_msg;
      return t.profiler_type === filterType;
    });
  }, [traces, filterType]);

  // Calculate percentiles (P50, P90, P99)
  const percentiles = useMemo(() => {
    if (traces.length === 0) return { p50: 0, p90: 0, p99: 0 };
    const durations = traces.map((t) => t.duration_ms).sort((a, b) => a - b);
    const getP = (pct: number) => {
      const idx = Math.floor(durations.length * pct);
      return durations[Math.min(idx, durations.length - 1)] || 0;
    };
    return {
      p50: getP(0.5),
      p90: getP(0.9),
      p99: getP(0.99),
    };
  }, [traces]);

  // Latency timeline bucketed data
  const timelineChartData = useMemo(() => {
    if (traces.length === 0) return [];
    const sorted = [...traces].sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
    return sorted.map((t, idx) => {
      const d = new Date(t.timestamp);
      return {
        id: idx + 1,
        time: `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`,
        duration: Number(t.duration_ms.toFixed(2)),
        isError: t.status_code >= 400 || !!t.error_msg ? 1 : 0,
        service: t.service_name,
        signature: t.node_signature || 'trace',
      };
    });
  }, [traces]);

  // Service Breakdown data
  const serviceChartData = useMemo(() => {
    const map = new Map<string, { service: string; count: number; totalDuration: number; errors: number }>();
    traces.forEach((t) => {
      const name = t.service_name || 'default';
      if (!map.has(name)) {
        map.set(name, { service: name, count: 0, totalDuration: 0, errors: 0 });
      }
      const s = map.get(name)!;
      s.count++;
      s.totalDuration += t.duration_ms;
      if (t.status_code >= 400 || !!t.error_msg) s.errors++;
    });
    return Array.from(map.values()).map((s) => ({
      service: s.service,
      avgLatency: Number((s.totalDuration / s.count).toFixed(2)),
      count: s.count,
      errors: s.errors,
    }));
  }, [traces]);

  return (
    <div className="space-y-6">
      {/* Header Banner */}
      <div className="rounded-xl border border-indigo-900/40 bg-gradient-to-r from-indigo-950/40 via-slate-900 to-purple-950/30 p-6">
        <div className="flex flex-col md:flex-row items-start md:items-center justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <Zap className="h-6 w-6 text-indigo-400" />
              <h2 className="text-xl font-bold text-white tracking-tight">
                Universal Runtime & Profiler Telemetry
              </h2>
            </div>
            <p className="mt-1 text-sm text-slate-400 max-w-2xl">
              Real-time ingestion for OpenTelemetry (OTLP), pprof, test runners, and coding agent execution profiles.
              Correlates runtime latency and CPU/memory hotspots directly with AST code churn.
            </p>
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={() => {
                tracesQuery.refetch();
                hotspotsQuery.refetch();
                overviewQuery.refetch();
              }}
              className="inline-flex items-center gap-1.5 rounded-lg border border-slate-700 bg-slate-800/80 px-3 py-1.5 text-xs font-medium text-slate-300 hover:bg-slate-700 hover:text-white transition"
            >
              <RefreshCw className="h-3.5 w-3.5" />
              Refresh
            </button>
          </div>
        </div>
      </div>

      {/* KPI Cards with Percentiles */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-4">
        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-400">Total Traces</span>
            <Activity className="h-4 w-4 text-indigo-400" />
          </div>
          <p className="mt-2 text-2xl font-bold text-white">
            {overview?.total_traces ?? traces.length}
          </p>
          <p className="text-xs text-slate-500 mt-1">Ingested runtime spans</p>
        </div>

        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-400">P50 Latency</span>
            <Clock className="h-4 w-4 text-emerald-400" />
          </div>
          <p className="mt-2 text-2xl font-bold text-emerald-400">
            {percentiles.p50.toFixed(1)} ms
          </p>
          <p className="text-xs text-slate-500 mt-1">Median span duration</p>
        </div>

        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-400">P90 / P99 Latency</span>
            <TrendingUp className="h-4 w-4 text-amber-400" />
          </div>
          <p className="mt-2 text-2xl font-bold text-amber-400">
            {percentiles.p90.toFixed(1)} <span className="text-xs text-slate-400 font-normal">/ {percentiles.p99.toFixed(1)} ms</span>
          </p>
          <p className="text-xs text-slate-500 mt-1">Tail latency thresholds</p>
        </div>

        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-400">Active Services</span>
            <Database className="h-4 w-4 text-cyan-400" />
          </div>
          <p className="mt-2 text-2xl font-bold text-white">
            {overview?.active_services ?? serviceChartData.length}
          </p>
          <p className="text-xs text-slate-500 mt-1">Observed services</p>
        </div>

        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-4">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-slate-400">Runtime Errors</span>
            <AlertTriangle className="h-4 w-4 text-rose-400" />
          </div>
          <p className="mt-2 text-2xl font-bold text-rose-400">
            {overview?.total_errors ?? traces.filter(t => t.status_code >= 400 || !!t.error_msg).length}
          </p>
          <p className="text-xs text-slate-500 mt-1">Status ≥ 400 or exceptions</p>
        </div>
      </div>

      {/* Latency & Runtime Charts */}
      {traces.length > 0 && (
        <div className="panel space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/5 pb-3">
            <div className="flex items-center gap-2">
              <BarChart3 className="h-4 w-4 text-indigo-400" />
              <h3 className="font-semibold tracking-tight text-sm text-white">
                Runtime Execution Latency Telemetry
              </h3>
            </div>
            <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
              <button
                onClick={() => setChartView('latency')}
                className={`px-2.5 py-1 rounded font-medium transition-all ${
                  chartView === 'latency' ? 'bg-indigo-600 text-white shadow-sm' : 'text-slate-400 hover:text-slate-200'
                }`}
              >
                Latency Timeline
              </button>
              <button
                onClick={() => setChartView('services')}
                className={`px-2.5 py-1 rounded font-medium transition-all ${
                  chartView === 'services' ? 'bg-indigo-600 text-white shadow-sm' : 'text-slate-400 hover:text-slate-200'
                }`}
              >
                Service Breakdown
              </button>
            </div>
          </div>

          <div className="h-56">
            <ResponsiveContainer width="100%" height="100%">
              {chartView === 'latency' ? (
                <AreaChart data={timelineChartData} margin={{ top: 8, right: 10, left: -10, bottom: 0 }}>
                  <defs>
                    <linearGradient id="latencyGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#818cf8" stopOpacity={0.4} />
                      <stop offset="95%" stopColor="#818cf8" stopOpacity={0.0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                  <XAxis dataKey="time" stroke="#64748b" tick={{ fontSize: 11 }} />
                  <YAxis stroke="#64748b" tick={{ fontSize: 11 }} unit="ms" />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#0f172a',
                      borderColor: 'rgba(255,255,255,0.1)',
                      borderRadius: 8,
                      fontSize: 12,
                      color: '#f8fafc',
                    }}
                    formatter={(val: any) => [`${val} ms`, 'Span Duration']}
                  />
                  <Area
                    type="monotone"
                    dataKey="duration"
                    name="Duration (ms)"
                    stroke="#818cf8"
                    strokeWidth={2}
                    fill="url(#latencyGrad)"
                  />
                </AreaChart>
              ) : (
                <BarChart data={serviceChartData} margin={{ top: 8, right: 10, left: -10, bottom: 0 }}>
                  <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                  <XAxis dataKey="service" stroke="#64748b" tick={{ fontSize: 11 }} />
                  <YAxis stroke="#64748b" tick={{ fontSize: 11 }} unit="ms" />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#0f172a',
                      borderColor: 'rgba(255,255,255,0.1)',
                      borderRadius: 8,
                      fontSize: 12,
                    }}
                  />
                  <Legend wrapperStyle={{ fontSize: 11 }} />
                  <Bar dataKey="avgLatency" name="Avg Latency (ms)" fill="#818cf8" radius={[4, 4, 0, 0]} />
                  <Bar dataKey="count" name="Call Count" fill="#38bdf8" radius={[4, 4, 0, 0]} />
                </BarChart>
              )}
            </ResponsiveContainer>
          </div>
        </div>
      )}

      {/* Grid: Hotspots & Live Trace Stream */}
      <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
        {/* Left Column: Latency Hotspots */}
        <div className="xl:col-span-1 space-y-4">
          <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-5">
            <div className="flex items-center gap-2 mb-4">
              <Flame className="h-5 w-5 text-amber-400" />
              <h3 className="text-base font-semibold text-white">Function Latency Hotspots</h3>
            </div>
            {hotspots.length === 0 ? (
              <div className="py-8 text-center text-xs text-slate-500">
                No runtime hotspots recorded yet. Send traces to <span className="font-mono text-slate-400">/v1/traces</span> or <span className="font-mono text-slate-400">/api/profiler/ingest</span>.
              </div>
            ) : (
              <div className="space-y-3">
                {hotspots.map((h, i) => (
                  <div
                    key={i}
                    className="p-3 rounded-lg border border-slate-800/80 bg-slate-950/40 hover:border-slate-700 transition"
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="truncate">
                        <p className="text-xs font-mono font-medium text-indigo-300 truncate" title={h.node_signature}>
                          {h.node_signature || 'anonymous_span'}
                        </p>
                        <p className="text-[11px] text-slate-500 truncate" title={h.file_path}>
                          {h.file_path || 'unknown file'}
                        </p>
                      </div>
                      <span className="shrink-0 text-xs font-bold text-amber-400 font-mono">
                        {h.avg_duration_ms.toFixed(1)} ms
                      </span>
                    </div>
                    <div className="mt-2 flex items-center justify-between text-[11px] text-slate-400 pt-2 border-t border-slate-800/50">
                      <span>{h.trace_count} calls</span>
                      {h.total_errors > 0 ? (
                        <span className="text-rose-400 font-medium">{h.total_errors} errors</span>
                      ) : (
                        <span className="text-emerald-400">0 errors</span>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>

        {/* Right Column: Live Traces Stream */}
        <div className="xl:col-span-2 space-y-4">
          <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-5">
            <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 mb-4">
              <div className="flex items-center gap-2">
                <Activity className="h-5 w-5 text-indigo-400" />
                <h3 className="text-base font-semibold text-white">Live Execution Traces</h3>
                <span className="rounded-full bg-indigo-950 px-2 py-0.5 text-[10px] font-medium text-indigo-400 border border-indigo-800/50">
                  {filteredTraces.length} events
                </span>
              </div>
              <div className="flex items-center gap-1.5 bg-slate-950 p-1 rounded-lg border border-slate-800">
                {['all', 'otlp', 'pprof', 'test_runner', 'errors'].map((type) => (
                  <button
                    key={type}
                    onClick={() => setFilterType(type)}
                    className={`px-2.5 py-1 text-xs rounded font-medium transition ${
                      filterType === type
                        ? 'bg-indigo-600 text-white shadow-sm'
                        : 'text-slate-400 hover:text-slate-200'
                    }`}
                  >
                    {type.toUpperCase()}
                  </button>
                ))}
              </div>
            </div>

            {filteredTraces.length === 0 ? (
              <div className="py-12 text-center text-xs text-slate-500">
                No matching runtime traces found.
              </div>
            ) : (
              <div className="space-y-2 max-h-[520px] overflow-y-auto pr-1">
                {filteredTraces.map((trace) => {
                  const isErr = trace.status_code >= 400 || !!trace.error_msg;
                  return (
                    <div
                      key={trace.trace_id}
                      onClick={() => setSelectedTrace(trace)}
                      className="cursor-pointer p-3 rounded-lg border border-slate-800/80 bg-slate-950/50 hover:border-indigo-500/50 transition flex items-center justify-between gap-4"
                    >
                      <div className="flex items-center gap-3 min-w-0">
                        {isErr ? (
                          <AlertTriangle className="h-4 w-4 text-rose-400 shrink-0" />
                        ) : (
                          <CheckCircle2 className="h-4 w-4 text-emerald-400 shrink-0" />
                        )}
                        <div className="min-w-0">
                          <div className="flex items-center gap-2">
                            <span className="text-xs font-mono font-medium text-slate-200 truncate">
                              {trace.node_signature || trace.service_name}
                            </span>
                            <span className="px-1.5 py-0.5 rounded text-[10px] font-mono bg-slate-800 text-slate-300">
                              {trace.profiler_type}
                            </span>
                          </div>
                          <p className="text-[11px] text-slate-500 truncate mt-0.5">
                            {trace.service_name} &bull; {trace.file_path || 'in-memory'}
                          </p>
                        </div>
                      </div>
                      <div className="flex items-center gap-4 shrink-0 text-right">
                        <div>
                          <p className="text-xs font-mono font-semibold text-white">
                            {trace.duration_ms.toFixed(1)} ms
                          </p>
                          <p className="text-[10px] text-slate-500">
                            {new Date(trace.timestamp).toLocaleTimeString()}
                          </p>
                        </div>
                        <span
                          className={`px-2 py-0.5 text-xs font-mono font-medium rounded ${
                            isErr ? 'bg-rose-950/80 text-rose-400 border border-rose-800/50' : 'bg-emerald-950/80 text-emerald-400 border border-emerald-800/50'
                          }`}
                        >
                          HTTP {trace.status_code}
                        </span>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Selected Trace Details Modal / Drawer */}
      {selectedTrace && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm">
          <div className="w-full max-w-2xl rounded-xl border border-slate-800 bg-slate-900 p-6 shadow-2xl space-y-4">
            <div className="flex items-center justify-between border-b border-slate-800 pb-3">
              <div className="flex items-center gap-2">
                <Activity className="h-5 w-5 text-indigo-400" />
                <h3 className="text-base font-semibold text-white">Trace Details: {selectedTrace.trace_id}</h3>
              </div>
              <button
                onClick={() => setSelectedTrace(null)}
                className="text-slate-400 hover:text-white text-xs px-2.5 py-1 bg-slate-800 rounded font-medium"
              >
                Close
              </button>
            </div>
            <div className="grid grid-cols-2 gap-3 text-xs">
              <div>
                <span className="text-slate-500">Service:</span> <span className="text-white font-mono">{selectedTrace.service_name}</span>
              </div>
              <div>
                <span className="text-slate-500">Duration:</span> <span className="text-white font-mono">{selectedTrace.duration_ms.toFixed(2)} ms</span>
              </div>
              <div>
                <span className="text-slate-500">Status:</span> <span className="text-white font-mono">{selectedTrace.status_code}</span>
              </div>
              <div>
                <span className="text-slate-500">Profiler:</span> <span className="text-white font-mono">{selectedTrace.profiler_type}</span>
              </div>
              <div className="col-span-2">
                <span className="text-slate-500">Signature:</span> <span className="text-indigo-300 font-mono">{selectedTrace.node_signature || 'N/A'}</span>
              </div>
              <div className="col-span-2">
                <span className="text-slate-500">File:</span> <span className="text-slate-300 font-mono">{selectedTrace.file_path || 'N/A'}</span>
              </div>
            </div>
            {selectedTrace.error_msg && (
              <div className="rounded-lg bg-rose-950/50 border border-rose-800 p-3 text-xs text-rose-300">
                <p className="font-semibold mb-1">Error Message:</p>
                <p className="font-mono">{selectedTrace.error_msg}</p>
              </div>
            )}
            {selectedTrace.metadata && (
              <div className="space-y-1">
                <p className="text-xs text-slate-400 font-semibold">Metadata & Attributes:</p>
                <pre className="p-3 bg-slate-950 rounded-lg text-slate-300 text-[11px] font-mono overflow-x-auto max-h-48 border border-slate-800">
                  {JSON.stringify(selectedTrace.metadata, null, 2)}
                </pre>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

