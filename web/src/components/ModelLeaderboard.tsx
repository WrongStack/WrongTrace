import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import type { ModelRow } from '../types';

interface ModelLeaderboardProps {
  models: ModelRow[];
  loading: boolean;
}

export function ModelLeaderboard({ models, loading }: ModelLeaderboardProps) {
  const data = models.map((m) => ({
    model: m.model,
    survival: Number(m.survival_rate_pct.toFixed(1)),
    costPerSurvived:
      Number.isFinite(m.cost_per_surviving_node) && m.cost_per_surviving_node < 1000
        ? Number(m.cost_per_surviving_node.toFixed(4))
        : 0,
  }));

  return (
    <div className="panel">
      <div className="flex items-center justify-between mb-3">
        <h2 className="font-semibold tracking-tight">Model Leaderboard</h2>
        <span className="text-xs text-slate-500">survival rate vs. cost per survived node</span>
      </div>

      {loading && <div className="text-sm text-slate-500">loading…</div>}

      {!loading && data.length === 0 && (
        <div className="text-sm text-slate-500 py-8 text-center">
          no model telemetry yet — connect an agent via IPC or MCP.
        </div>
      )}

      {data.length > 0 && (
        <div className="h-64">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={data} margin={{ top: 8, right: 12, left: 0, bottom: 4 }}>
              <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
              <XAxis dataKey="model" stroke="#94a3b8" tick={{ fontSize: 11 }} />
              <YAxis stroke="#94a3b8" tick={{ fontSize: 11 }} domain={[0, 100]} />
              <Tooltip
                contentStyle={{
                  background: '#161a23',
                  border: '1px solid rgba(255,255,255,0.08)',
                  borderRadius: 8,
                  fontSize: 12,
                }}
              />
              <Bar dataKey="survival" fill="#6366f1" radius={[4, 4, 0, 0]} name="Survival %" />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <table className="mt-4 w-full text-xs">
        <thead className="text-slate-400">
          <tr>
            <th className="text-left font-medium py-1">Model</th>
            <th className="text-right font-medium py-1">Survival</th>
            <th className="text-right font-medium py-1">Runs</th>
            <th className="text-right font-medium py-1">Cost / Survived</th>
            <th className="text-right font-medium py-1">Total Cost</th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {models.map((m) => (
            <tr key={m.model} className="border-t border-white/5">
              <td className="py-1.5 text-slate-200">{m.model}</td>
              <td className="py-1.5 text-right text-accent">{m.survival_rate_pct.toFixed(1)}%</td>
              <td className="py-1.5 text-right text-slate-300">{m.run_count}</td>
              <td className="py-1.5 text-right text-slate-300">
                ${m.cost_per_surviving_node.toFixed(4)}
              </td>
              <td className="py-1.5 text-right text-slate-400">
                ${m.total_cost_usd.toFixed(2)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
