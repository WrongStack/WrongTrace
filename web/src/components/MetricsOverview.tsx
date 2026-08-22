import { Activity, DollarSign, Flame, Trophy, TrendingUp, Sparkles, Layers } from 'lucide-react';
import type { Overview, ModelRow, ThrashingRow } from '../types';

interface MetricsOverviewProps {
  overview?: Overview;
  thrashing: ThrashingRow[];
  models: ModelRow[];
  loading: boolean;
}

function fmtUSD(n: number): string {
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
}

export function MetricsOverview({ overview, thrashing, models, loading }: MetricsOverviewProps) {
  const totalSpend = models.reduce((acc, m) => acc + m.total_cost_usd, 0);
  const wastedSpend = models.reduce(
    (acc, m) =>
      acc +
      (m.total_nodes > 0
        ? m.total_cost_usd * ((m.total_nodes - m.active_nodes) / m.total_nodes)
        : m.total_cost_usd),
    0,
  );
  const totalSurvived = models.reduce((acc, m) => acc + m.total_survived_nodes, 0);
  const totalNodes = models.reduce((acc, m) => acc + m.total_nodes, 0);
  const globalSurvivalPct = totalNodes > 0 ? (totalSurvived / totalNodes) * 100 : 100;

  const topModel = [...models]
    .filter((m) => m.total_survived_nodes > 0)
    .sort((a, b) => b.survival_rate_pct - a.survival_rate_pct)[0];

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
      {/* 1. Total Agent Runs */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-indigo-500/40 transition-all hover:shadow-lg hover:shadow-indigo-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">Total Agent Runs</span>
          <div className="p-2 rounded-lg bg-indigo-500/15 text-indigo-400 group-hover:scale-110 transition-transform">
            <Activity className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">
          {loading ? '…' : (overview?.TotalRuns ?? 0).toLocaleString()}
        </div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>{loading ? '…' : (overview?.TotalEvents ?? 0).toLocaleString()} AST events</span>
          <span className="text-[10px] text-emerald-400 font-mono flex items-center gap-0.5">
            <TrendingUp className="h-3 w-3" /> Live
          </span>
        </div>
      </div>

      {/* 2. Thrashing Files Alert */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-amber-500/40 transition-all hover:shadow-lg hover:shadow-amber-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">Thrashing Code Nodes</span>
          <div className={`p-2 rounded-lg ${thrashing.length > 0 ? 'bg-amber-500/20 text-amber-400 animate-pulse' : 'bg-slate-800 text-slate-400'} group-hover:scale-110 transition-transform`}>
            <Flame className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">{thrashing.length}</div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>≥3 edits in 24h window</span>
          {thrashing.length > 0 ? (
            <span className="text-[10px] text-amber-400 font-mono font-medium">Needs Attention</span>
          ) : (
            <span className="text-[10px] text-emerald-400 font-mono">Calm Codebase</span>
          )}
        </div>
      </div>

      {/* 3. Spend vs Wasted */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-rose-500/40 transition-all hover:shadow-lg hover:shadow-rose-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">AI Spend & Waste</span>
          <div className="p-2 rounded-lg bg-rose-500/15 text-rose-400 group-hover:scale-110 transition-transform">
            <DollarSign className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">{fmtUSD(totalSpend)}</div>
        <div className="mt-2 flex items-center justify-between text-xs">
          <span className="text-slate-400">Wasted churn:</span>
          <span className="text-rose-400 font-mono font-medium">{fmtUSD(Math.max(0, wastedSpend))}</span>
        </div>
      </div>

      {/* 4. Top Performing Model */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-yellow-500/40 transition-all hover:shadow-lg hover:shadow-yellow-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">Top AI Model</span>
          <div className="p-2 rounded-lg bg-yellow-500/15 text-yellow-400 group-hover:scale-110 transition-transform">
            <Trophy className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num truncate text-white">
          {topModel ? topModel.model : '—'}
        </div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>
            {topModel ? `${topModel.survival_rate_pct.toFixed(1)}% survival` : 'Awaiting data'}
          </span>
          {topModel && (
            <span className="text-[10px] text-accent font-mono">
              ${topModel.cost_per_surviving_node.toFixed(4)}/node
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
