import { useState, useMemo } from 'react';
import {
  ResponsiveContainer,
  ComposedChart,
  Area,
  Bar,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  CartesianGrid,
  Legend,
  PieChart,
  Pie,
  Cell,
} from 'recharts';
import {
  TrendingUp,
  GitCommit,
  PlusCircle,
  MinusCircle,
  Clock,
  Layers,
  PieChart as PieIcon,
} from 'lucide-react';
import type { EventRecord } from '../types';

interface CodeChurnTimelineProps {
  events: EventRecord[];
  loading?: boolean;
}

const ACTION_COLORS = {
  ADDED: '#10b981',    // emerald
  MODIFIED: '#f59e0b', // amber
  DELETED: '#ef4444',  // red
};

export function CodeChurnTimeline({ events = [], loading = false }: CodeChurnTimelineProps) {
  const [timeFilter, setTimeFilter] = useState<'all' | '24h' | '7d'>('all');
  const [chartMetric, setChartMetric] = useState<'lines' | 'events'>('lines');

  // Filter events based on selected time window
  const filteredEvents = useMemo(() => {
    if (!events || events.length === 0) return [];
    if (timeFilter === 'all') return events;
    const now = Date.now();
    const windowMs = timeFilter === '24h' ? 24 * 3600 * 1000 : 7 * 24 * 3600 * 1000;
    return events.filter((e) => {
      const t = Date.parse(e.event_time);
      return !Number.isNaN(t) && now - t <= windowMs;
    });
  }, [events, timeFilter]);

  // Aggregate events into adaptive time buckets for chart
  const timelineData = useMemo(() => {
    if (filteredEvents.length === 0) return [];

    // Pre-parse timestamps once to avoid repeated O(N log N) Date.parse calls in sort
    const mapped: Array<{ event: EventRecord; time: number }> = [];
    for (let i = 0; i < filteredEvents.length; i++) {
      const e = filteredEvents[i];
      const t = Date.parse(e.event_time);
      if (!Number.isNaN(t)) {
        mapped.push({ event: e, time: t });
      }
    }

    if (mapped.length === 0) return [];
    mapped.sort((a, b) => a.time - b.time);

    const firstTime = mapped[0].time;
    const lastTime = mapped[mapped.length - 1].time;
    const spanMs = Math.max(lastTime - firstTime, 1000);

    // Adaptive bucket granularity:
    // < 4 hours: 15-minute buckets
    // < 48 hours: 1-hour buckets
    // >= 48 hours: 1-day buckets
    const bucketMode =
      timeFilter === '24h' || spanMs <= 4 * 3600 * 1000
        ? '15m'
        : spanMs <= 48 * 3600 * 1000
        ? '1h'
        : '1d';

    const buckets = new Map<
      string,
      {
        timeKey: string;
        label: string;
        addedLines: number;
        deletedLines: number;
        netLines: number;
        eventsCount: number;
        addedEvents: number;
        modifiedEvents: number;
        deletedEvents: number;
      }
    >();

    mapped.forEach(({ event: e, time: t }) => {
      const d = new Date(t);
      let timeKey: string;
      let label: string;

      if (bucketMode === '15m') {
        const m = Math.floor(d.getMinutes() / 15) * 15;
        timeKey = `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
        label = `${String(d.getHours()).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
      } else if (bucketMode === '1h') {
        timeKey = `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:00`;
        label = `${d.toLocaleDateString(undefined, { month: 'numeric', day: 'numeric' })} ${String(d.getHours()).padStart(2, '0')}:00`;
      } else {
        timeKey = `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`;
        label = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
      }

      if (!buckets.has(timeKey)) {
        buckets.set(timeKey, {
          timeKey,
          label,
          addedLines: 0,
          deletedLines: 0,
          netLines: 0,
          eventsCount: 0,
          addedEvents: 0,
          modifiedEvents: 0,
          deletedEvents: 0,
        });
      }

      const b = buckets.get(timeKey)!;
      const added =
        typeof e.added_lines === 'number' && e.added_lines > 0
          ? e.added_lines
          : e.action === 'ADDED'
          ? e.lines_of_code || 10
          : e.action === 'MODIFIED'
          ? Math.max(1, Math.round((e.lines_of_code || 6) * 0.3))
          : 0;

      const deleted =
        typeof e.deleted_lines === 'number' && e.deleted_lines > 0
          ? e.deleted_lines
          : e.action === 'DELETED'
          ? e.lines_of_code || 10
          : 0;

      b.addedLines += added;
      b.deletedLines += deleted;
      b.netLines += added - deleted;
      b.eventsCount += 1;

      if (e.action === 'ADDED') b.addedEvents += 1;
      else if (e.action === 'MODIFIED') b.modifiedEvents += 1;
      else if (e.action === 'DELETED') b.deletedEvents += 1;
    });

    const result = Array.from(buckets.values());
    return result;
  }, [filteredEvents, timeFilter]);

  // Overall KPI aggregates
  const totals = useMemo(() => {
    let added = 0;
    let deleted = 0;
    let addedEv = 0;
    let modEv = 0;
    let delEv = 0;

    filteredEvents.forEach((e) => {
      const add =
        typeof e.added_lines === 'number' && e.added_lines > 0
          ? e.added_lines
          : e.action === 'ADDED'
          ? e.lines_of_code || 10
          : e.action === 'MODIFIED'
          ? Math.max(1, Math.round((e.lines_of_code || 6) * 0.3))
          : 0;

      const del =
        typeof e.deleted_lines === 'number' && e.deleted_lines > 0
          ? e.deleted_lines
          : e.action === 'DELETED'
          ? e.lines_of_code || 10
          : 0;

      added += add;
      deleted += del;
      if (e.action === 'ADDED') addedEv++;
      else if (e.action === 'MODIFIED') modEv++;
      else if (e.action === 'DELETED') delEv++;
    });

    return {
      added,
      deleted,
      net: added - deleted,
      totalEvents: filteredEvents.length,
      actionDistribution: [
        { name: 'Added', value: addedEv, color: ACTION_COLORS.ADDED },
        { name: 'Modified', value: modEv, color: ACTION_COLORS.MODIFIED },
        { name: 'Deleted', value: delEv, color: ACTION_COLORS.DELETED },
      ].filter((x) => x.value > 0),
    };
  }, [filteredEvents]);

  return (
    <div className="panel space-y-4">
      {/* Header & Controls */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/5 pb-3">
        <div className="flex items-center gap-2.5">
          <div className="p-2 rounded-lg bg-indigo-500/15 text-indigo-400">
            <TrendingUp className="h-4 w-4" />
          </div>
          <div>
            <h2 className="font-semibold tracking-tight text-sm flex items-center gap-2">
              AST Code Churn & Velocity Timeline
              <span className="text-xs font-normal text-slate-400 font-mono">
                ({filteredEvents.length} events observed)
              </span>
            </h2>
            <p className="text-xs text-slate-400">
              Correlated lines of code and AST semantic mutations generated across agent sessions.
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2 flex-wrap">
          {/* Metric Selector */}
          <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
            <button
              onClick={() => setChartMetric('lines')}
              className={`px-2.5 py-1 rounded font-medium transition-all ${
                chartMetric === 'lines'
                  ? 'bg-accent text-white shadow-sm'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              Lines Churn
            </button>
            <button
              onClick={() => setChartMetric('events')}
              className={`px-2.5 py-1 rounded font-medium transition-all ${
                chartMetric === 'events'
                  ? 'bg-accent text-white shadow-sm'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              Event Volume
            </button>
          </div>

          {/* Time Window Selector */}
          <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
            {(['all', '7d', '24h'] as const).map((mode) => (
              <button
                key={mode}
                onClick={() => setTimeFilter(mode)}
                className={`px-2.5 py-1 rounded font-medium uppercase font-mono text-[10px] transition-all ${
                  timeFilter === mode
                    ? 'bg-accent text-white font-bold shadow-sm'
                    : 'text-slate-400 hover:text-slate-200'
                }`}
              >
                {mode === 'all' ? 'All Time' : mode}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* KPI Highlights Bar */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <div className="panel-raised p-3 border-l-2 border-l-emerald-500">
          <div className="flex items-center justify-between text-xs text-slate-400">
            <span>Lines Added</span>
            <PlusCircle className="h-3.5 w-3.5 text-emerald-400" />
          </div>
          <div className="mt-1 font-mono text-lg font-bold text-emerald-400">
            +{totals.added.toLocaleString()}
          </div>
          <div className="text-[10px] text-slate-500 mt-0.5">AST inserted code</div>
        </div>

        <div className="panel-raised p-3 border-l-2 border-l-rose-500">
          <div className="flex items-center justify-between text-xs text-slate-400">
            <span>Lines Deleted</span>
            <MinusCircle className="h-3.5 w-3.5 text-rose-400" />
          </div>
          <div className="mt-1 font-mono text-lg font-bold text-rose-400">
            -{totals.deleted.toLocaleString()}
          </div>
          <div className="text-[10px] text-slate-500 mt-0.5">AST pruned code</div>
        </div>

        <div className="panel-raised p-3 border-l-2 border-l-cyan-500">
          <div className="flex items-center justify-between text-xs text-slate-400">
            <span>Net LOC Growth</span>
            <GitCommit className="h-3.5 w-3.5 text-cyan-400" />
          </div>
          <div className={`mt-1 font-mono text-lg font-bold ${totals.net >= 0 ? 'text-cyan-400' : 'text-amber-400'}`}>
            {totals.net >= 0 ? `+${totals.net.toLocaleString()}` : totals.net.toLocaleString()}
          </div>
          <div className="text-[10px] text-slate-500 mt-0.5">Surviving codebase delta</div>
        </div>

        <div className="panel-raised p-3 border-l-2 border-l-amber-500">
          <div className="flex items-center justify-between text-xs text-slate-400">
            <span>AST Operations</span>
            <Layers className="h-3.5 w-3.5 text-amber-400" />
          </div>
          <div className="mt-1 font-mono text-lg font-bold text-slate-200">
            {totals.totalEvents.toLocaleString()}
          </div>
          <div className="text-[10px] text-slate-500 mt-0.5">Functions, classes & structs</div>
        </div>
      </div>

      {/* Main Chart Section */}
      {loading ? (
        <div className="h-64 flex items-center justify-center text-xs text-slate-500">
          Loading churn timeline…
        </div>
      ) : timelineData.length === 0 ? (
        <div className="h-48 flex flex-col items-center justify-center text-xs text-slate-500 border border-dashed border-white/10 rounded-xl">
          <Clock className="h-6 w-6 text-slate-600 mb-2" />
          <span>No code transitions recorded in this time window.</span>
          <span className="text-[11px] text-slate-600 mt-0.5">
            Modify files or run an agent to observe live AST churn.
          </span>
        </div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-12 gap-4 items-center">
          {/* Main Composed Chart */}
          <div className="lg:col-span-9 h-64">
            <ResponsiveContainer width="100%" height="100%">
              {chartMetric === 'lines' ? (
                <ComposedChart data={timelineData} margin={{ top: 10, right: 10, left: -15, bottom: 0 }}>
                  <defs>
                    <linearGradient id="addedGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#10b981" stopOpacity={0.4} />
                      <stop offset="95%" stopColor="#10b981" stopOpacity={0.0} />
                    </linearGradient>
                    <linearGradient id="deletedGrad" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#ef4444" stopOpacity={0.4} />
                      <stop offset="95%" stopColor="#ef4444" stopOpacity={0.0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} strokeDasharray="3 3" />
                  <XAxis dataKey="label" stroke="#64748b" tick={{ fontSize: 11 }} />
                  <YAxis stroke="#64748b" tick={{ fontSize: 11 }} />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#0f172a',
                      borderColor: 'rgba(255,255,255,0.1)',
                      borderRadius: '8px',
                      fontSize: '12px',
                      color: '#f8fafc',
                    }}
                  />
                  <Legend
                    wrapperStyle={{ fontSize: '11px', paddingTop: '8px' }}
                    iconType="circle"
                  />
                  <Area
                    type="monotone"
                    dataKey="addedLines"
                    name="Added Lines"
                    stroke="#10b981"
                    strokeWidth={2}
                    fill="url(#addedGrad)"
                  />
                  <Area
                    type="monotone"
                    dataKey="deletedLines"
                    name="Deleted Lines"
                    stroke="#ef4444"
                    strokeWidth={2}
                    fill="url(#deletedGrad)"
                  />
                  <Line
                    type="monotone"
                    dataKey="netLines"
                    name="Net LOC Delta"
                    stroke="#38bdf8"
                    strokeWidth={2}
                    dot={{ r: 3, fill: '#38bdf8' }}
                  />
                </ComposedChart>
              ) : (
                <ComposedChart data={timelineData} margin={{ top: 10, right: 10, left: -15, bottom: 0 }}>
                  <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} strokeDasharray="3 3" />
                  <XAxis dataKey="label" stroke="#64748b" tick={{ fontSize: 11 }} />
                  <YAxis stroke="#64748b" tick={{ fontSize: 11 }} />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#0f172a',
                      borderColor: 'rgba(255,255,255,0.1)',
                      borderRadius: '8px',
                      fontSize: '12px',
                      color: '#f8fafc',
                    }}
                  />
                  <Legend
                    wrapperStyle={{ fontSize: '11px', paddingTop: '8px' }}
                    iconType="circle"
                  />
                  <Bar dataKey="addedEvents" name="Node Additions" fill="#10b981" stackId="a" radius={[0, 0, 0, 0]} />
                  <Bar dataKey="modifiedEvents" name="Node Modifications" fill="#f59e0b" stackId="a" radius={[0, 0, 0, 0]} />
                  <Bar dataKey="deletedEvents" name="Node Deletions" fill="#ef4444" stackId="a" radius={[4, 4, 0, 0]} />
                </ComposedChart>
              )}
            </ResponsiveContainer>
          </div>

          {/* Right Action Distribution Donut & Stats */}
          <div className="lg:col-span-3 panel-raised p-3 space-y-3 flex flex-col justify-between h-64">
            <div className="flex items-center justify-between text-xs text-slate-400">
              <span className="font-semibold text-slate-300 flex items-center gap-1.5">
                <PieIcon className="h-3.5 w-3.5 text-accent" />
                Action Split
              </span>
              <span className="text-[10px] font-mono">{totals.totalEvents} ops</span>
            </div>

            <div className="h-32 relative">
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie
                    data={totals.actionDistribution}
                    innerRadius={30}
                    outerRadius={48}
                    paddingAngle={4}
                    dataKey="value"
                  >
                    {totals.actionDistribution.map((entry, index) => (
                      <Cell key={`cell-${index}`} fill={entry.color} />
                    ))}
                  </Pie>
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#0f172a',
                      borderColor: 'rgba(255,255,255,0.1)',
                      borderRadius: '6px',
                      fontSize: '11px',
                    }}
                  />
                </PieChart>
              </ResponsiveContainer>
              <div className="absolute inset-0 flex flex-col items-center justify-center pointer-events-none">
                <span className="text-[10px] text-slate-500 font-mono">Total</span>
                <span className="text-xs font-bold text-white">{totals.totalEvents}</span>
              </div>
            </div>

            <div className="space-y-1 text-[11px] font-mono">
              {totals.actionDistribution.map((item) => (
                <div key={item.name} className="flex items-center justify-between text-slate-300">
                  <div className="flex items-center gap-1.5">
                    <span className="h-2 w-2 rounded-full" style={{ backgroundColor: item.color }} />
                    <span className="text-slate-400">{item.name}:</span>
                  </div>
                  <span className="font-semibold">
                    {item.value} ({totals.totalEvents > 0 ? ((item.value / totals.totalEvents) * 100).toFixed(0) : 0}%)
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
