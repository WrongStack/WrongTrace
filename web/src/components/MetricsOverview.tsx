import { Activity, DollarSign, Flame, Trophy } from 'lucide-react';
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
    (acc, m) => acc + (m.total_cost_usd - m.cost_per_surviving_node * m.total_survived_nodes),
    0,
  );
  const topModel = [...models].sort((a, b) => b.survival_rate_pct - a.survival_rate_pct)[0];

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
      <div className="panel">
        <div className="flex items-center justify-between">
          <span className="stat-label">Total Agent Runs</span>
          <Activity className="h-4 w-4 text-accent" />
        </div>
        <div className="mt-2 stat-num">{loading ? '…' : (overview?.TotalRuns ?? 0).toLocaleString()}</div>
        <div className="mt-1 text-xs text-slate-400">
          {loading ? '' : (overview?.TotalEvents ?? 0).toLocaleString()} AST events recorded
        </div>
      </div>

      <div className="panel">
        <div className="flex items-center justify-between">
          <span className="stat-label">Active Thrashing Files</span>
          <Flame className="h-4 w-4 text-signal-modified" />
        </div>
        <div className="mt-2 stat-num">{thrashing.length}</div>
        <div className="mt-1 text-xs text-slate-400">edited ≥3× in a 24h window</div>
      </div>

      <div className="panel">
        <div className="flex items-center justify-between">
          <span className="stat-label">Total Spend · Wasted</span>
          <DollarSign className="h-4 w-4 text-signal-deleted" />
        </div>
        <div className="mt-2 stat-num">{fmtUSD(totalSpend)}</div>
        <div className="mt-1 text-xs text-slate-400">
          <span className="text-signal-deleted">{fmtUSD(Math.max(0, wastedSpend))}</span> on churned nodes
        </div>
      </div>

      <div className="panel">
        <div className="flex items-center justify-between">
          <span className="stat-label">Top Surviving Model</span>
          <Trophy className="h-4 w-4 text-yellow-400" />
        </div>
        <div className="mt-2 stat-num truncate">{topModel ? topModel.model : '—'}</div>
        <div className="mt-1 text-xs text-slate-400">
          {topModel ? `${topModel.survival_rate_pct.toFixed(1)}% survival · ${topModel.total_survived_nodes} nodes` : 'no data yet'}
        </div>
      </div>
    </div>
  );
}
