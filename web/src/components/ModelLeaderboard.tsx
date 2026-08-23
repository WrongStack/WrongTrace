import { useState } from 'react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  Legend,
  ComposedChart,
  Line,
} from 'recharts';
import { Trophy, TrendingUp, DollarSign, Award, Layers } from 'lucide-react';
import type { ModelRow } from '../types';
import { isJunkModel } from '../types';

interface ModelLeaderboardProps {
  models: ModelRow[];
  loading: boolean;
}

export function ModelLeaderboard({ models, loading }: ModelLeaderboardProps) {
  const [viewMetric, setViewMetric] = useState<'survival' | 'roi' | 'multi'>('multi');

  const validModels = models.filter((m) => !isJunkModel(m.model));
  const sortedModels = [...validModels].sort((a, b) => b.survival_rate_pct - a.survival_rate_pct);

  const data = sortedModels.map((m) => ({
    model: m.model.length > 18 ? `${m.model.slice(0, 16)}…` : m.model,
    fullModel: m.model,
    survival: Number(m.survival_rate_pct.toFixed(1)),
    runs: m.run_count,
    costPerSurvived:
      Number.isFinite(m.cost_per_surviving_node) && m.cost_per_surviving_node < 1000
        ? Number(m.cost_per_surviving_node.toFixed(4))
        : 0,
    totalCost: Number(m.total_cost_usd.toFixed(3)),
    activeNodes: m.active_nodes,
    survivedNodes: m.total_survived_nodes,
  }));

  return (
    <div className="panel space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/5 pb-3">
        <div className="flex items-center gap-2">
          <Trophy className="h-4 w-4 text-yellow-400" />
          <h2 className="font-semibold tracking-tight text-sm flex items-center gap-2">
            Model Leaderboard & Quality Index
          </h2>
        </div>

        {/* View Metric Mode Toggle */}
        <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
          <button
            onClick={() => setViewMetric('multi')}
            className={`px-2.5 py-1 rounded font-medium transition-all ${
              viewMetric === 'multi'
                ? 'bg-accent text-white shadow-sm'
                : 'text-slate-400 hover:text-slate-200'
            }`}
          >
            Multi-Metric
          </button>
          <button
            onClick={() => setViewMetric('survival')}
            className={`px-2.5 py-1 rounded font-medium transition-all ${
              viewMetric === 'survival'
                ? 'bg-accent text-white shadow-sm'
                : 'text-slate-400 hover:text-slate-200'
            }`}
          >
            Survival %
          </button>
          <button
            onClick={() => setViewMetric('roi')}
            className={`px-2.5 py-1 rounded font-medium transition-all ${
              viewMetric === 'roi'
                ? 'bg-accent text-white shadow-sm'
                : 'text-slate-400 hover:text-slate-200'
            }`}
          >
            Cost / Node
          </button>
        </div>
      </div>

      {loading && <div className="text-sm text-slate-500 py-6 text-center">Loading model metrics…</div>}

      {!loading && data.length === 0 && (
        <div className="text-sm text-slate-500 py-8 text-center">
          No model telemetry yet — connect an agent via IPC, MCP, or Proxy.
        </div>
      )}

      {data.length > 0 && (
        <div className="h-64">
          <ResponsiveContainer width="100%" height="100%">
            {viewMetric === 'multi' ? (
              <ComposedChart data={data} margin={{ top: 8, right: 12, left: -10, bottom: 4 }}>
                <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                <XAxis dataKey="model" stroke="#94a3b8" tick={{ fontSize: 11 }} />
                <YAxis yAxisId="left" stroke="#818cf8" tick={{ fontSize: 11 }} domain={[0, 100]} />
                <YAxis yAxisId="right" orientation="right" stroke="#34d399" tick={{ fontSize: 11 }} />
                <Tooltip
                  contentStyle={{
                    background: '#0f172a',
                    border: '1px solid rgba(255,255,255,0.1)',
                    borderRadius: 8,
                    fontSize: 12,
                    color: '#f8fafc',
                  }}
                  formatter={(value: any, name: any) => {
                    if (name === 'Survival Rate') return [`${value}%`, name];
                    if (name === 'Total Spend') return [`$${value}`, name];
                    if (name === 'Cost / Survived') return [`$${value}`, name];
                    return [value, name];
                  }}
                />
                <Legend wrapperStyle={{ fontSize: 11, paddingTop: 4 }} />
                <Bar
                  yAxisId="left"
                  dataKey="survival"
                  fill="#6366f1"
                  radius={[4, 4, 0, 0]}
                  name="Survival Rate"
                />
                <Line
                  yAxisId="right"
                  type="monotone"
                  dataKey="costPerSurvived"
                  stroke="#34d399"
                  strokeWidth={2}
                  name="Cost / Survived"
                  dot={{ r: 4, fill: '#34d399' }}
                />
              </ComposedChart>
            ) : viewMetric === 'survival' ? (
              <BarChart data={data} margin={{ top: 8, right: 12, left: -10, bottom: 4 }}>
                <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                <XAxis dataKey="model" stroke="#94a3b8" tick={{ fontSize: 11 }} />
                <YAxis stroke="#94a3b8" tick={{ fontSize: 11 }} domain={[0, 100]} />
                <Tooltip
                  contentStyle={{
                    background: '#0f172a',
                    border: '1px solid rgba(255,255,255,0.1)',
                    borderRadius: 8,
                    fontSize: 12,
                  }}
                  formatter={(val: any) => [`${val}%`, 'Survival Rate']}
                />
                <Bar dataKey="survival" fill="#6366f1" radius={[4, 4, 0, 0]} name="Survival %" />
              </BarChart>
            ) : (
              <BarChart data={data} margin={{ top: 8, right: 12, left: -10, bottom: 4 }}>
                <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                <XAxis dataKey="model" stroke="#94a3b8" tick={{ fontSize: 11 }} />
                <YAxis stroke="#34d399" tick={{ fontSize: 11 }} />
                <Tooltip
                  contentStyle={{
                    background: '#0f172a',
                    border: '1px solid rgba(255,255,255,0.1)',
                    borderRadius: 8,
                    fontSize: 12,
                  }}
                  formatter={(val: any) => [`$${val}`, 'Cost per Survived Node']}
                />
                <Bar dataKey="costPerSurvived" fill="#10b981" radius={[4, 4, 0, 0]} name="Cost / Node ($)" />
              </BarChart>
            )}
          </ResponsiveContainer>
        </div>
      )}

      {/* Model Roster Table with Rank Badges */}
      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <thead className="text-slate-400 border-b border-white/5">
            <tr>
              <th className="text-left font-medium py-1.5 w-8">#</th>
              <th className="text-left font-medium py-1.5">Model</th>
              <th className="text-right font-medium py-1.5">Survival Rate</th>
              <th className="text-right font-medium py-1.5">Runs</th>
              <th className="text-right font-medium py-1.5">Cost / Survived</th>
              <th className="text-right font-medium py-1.5">Total Cost</th>
            </tr>
          </thead>
          <tbody className="font-mono divide-y divide-white/5">
            {sortedModels.map((m, idx) => (
              <tr key={m.model} className="hover:bg-white/[0.02] transition-colors">
                <td className="py-2 text-slate-400">
                  {idx === 0 ? (
                    <span className="text-amber-400 font-bold">1</span>
                  ) : idx === 1 ? (
                    <span className="text-slate-300 font-bold">2</span>
                  ) : idx === 2 ? (
                    <span className="text-amber-600 font-bold">3</span>
                  ) : (
                    idx + 1
                  )}
                </td>
                <td className="py-2 text-slate-200 font-semibold truncate max-w-[160px]" title={m.model}>
                  {m.model}
                </td>
                <td className="py-2 text-right">
                  <span className={`px-1.5 py-0.5 rounded text-[11px] font-semibold ${
                    m.survival_rate_pct >= 80
                      ? 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/20'
                      : m.survival_rate_pct >= 50
                      ? 'bg-amber-500/15 text-amber-400 border border-amber-500/20'
                      : 'bg-rose-500/15 text-red-400 border border-rose-500/20'
                  }`}>
                    {m.survival_rate_pct.toFixed(1)}%
                  </span>
                </td>
                <td className="py-2 text-right text-slate-300">{m.run_count}</td>
                <td className="py-2 text-right text-accent font-medium">
                  ${m.cost_per_surviving_node.toFixed(4)}
                </td>
                <td className="py-2 text-right text-slate-400">
                  ${m.total_cost_usd.toFixed(2)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

