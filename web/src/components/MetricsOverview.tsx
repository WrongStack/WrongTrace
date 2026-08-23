import { Activity, DollarSign, Flame, Trophy, TrendingUp, Boxes, Eye, Sparkles } from 'lucide-react';
import type { Overview, ModelRow, ThrashingRow, Project } from '../types';
import { isJunkModel } from '../types';
import { useAtlasStatus, useRecentReads } from '../hooks/useMetrics';

interface MetricsOverviewProps {
  overview?: Overview;
  thrashing: ThrashingRow[];
  models: ModelRow[];
  loading: boolean;
  currentProject?: Project | null;
}

function fmtUSD(n: number): string {
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
}

export function MetricsOverview({ overview, thrashing, models, loading, currentProject }: MetricsOverviewProps) {
  const { data: indexStatus } = useAtlasStatus(currentProject?.id);
  const { data: recentReads = [] } = useRecentReads(100, currentProject?.id);

  const validModels = models.filter((m) => !isJunkModel(m.model));
  const totalSpend = validModels.reduce((acc, m) => acc + m.total_cost_usd, 0);
  const wastedSpend = validModels.reduce(
    (acc, m) =>
      acc +
      (m.total_nodes > 0
        ? m.total_cost_usd * ((m.total_nodes - m.active_nodes) / m.total_nodes)
        : m.total_cost_usd),
    0,
  );

  const topModel = [...validModels]
    .filter((m) => m.total_survived_nodes > 0)
    .sort((a, b) => b.survival_rate_pct - a.survival_rate_pct)[0];

  const totalReadCount = recentReads.length;
  const totalReadTokens = recentReads.reduce((acc, r) => acc + (r.prompt_tokens || 0), 0);
  const totalReadCost = recentReads.reduce((acc, r) => acc + (r.cost_usd || 0), 0);

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6 gap-4">
      {/* 1. Total Agent Runs */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-indigo-500/40 transition-all hover:shadow-lg hover:shadow-indigo-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">Agent Runs</span>
          <div className="p-2 rounded-lg bg-indigo-500/15 text-indigo-400 group-hover:scale-110 transition-transform">
            <Activity className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">
          {loading ? '…' : (overview?.TotalRuns ?? 0).toLocaleString()}
        </div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>{(overview?.TotalEvents ?? 0).toLocaleString()} AST edits</span>
          <span className="text-[10px] text-emerald-400 font-mono flex items-center gap-0.5">
            <TrendingUp className="h-3 w-3" /> Live
          </span>
        </div>
      </div>

      {/* 2. Codebase Index Completeness */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-cyan-500/40 transition-all hover:shadow-lg hover:shadow-cyan-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">Index Completeness</span>
          <div className="p-2 rounded-lg bg-cyan-500/15 text-cyan-400 group-hover:scale-110 transition-transform">
            <Boxes className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">
          {indexStatus ? `${indexStatus.percentage.toFixed(0)}%` : '100%'}
        </div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>
            {indexStatus ? `${indexStatus.indexed_files}/${indexStatus.eligible_files} files` : 'AST Synchronized'}
          </span>
          <span className="text-[10px] text-cyan-400 font-mono">
            {indexStatus?.is_indexing ? 'Scanning…' : '100% Ready'}
          </span>
        </div>
      </div>

      {/* 3. File Reads & Context Tracing */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-purple-500/40 transition-all hover:shadow-lg hover:shadow-purple-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">File Reads (Context)</span>
          <div className="p-2 rounded-lg bg-purple-500/15 text-purple-400 group-hover:scale-110 transition-transform">
            <Eye className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">{totalReadCount}</div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>{totalReadTokens > 0 ? `${(totalReadTokens / 1000).toFixed(1)}k tokens` : 'view_file / read'}</span>
          <span className="text-[10px] text-purple-400 font-mono">
            {totalReadCost > 0 ? fmtUSD(totalReadCost) : '$0.00'}
          </span>
        </div>
      </div>

      {/* 4. Thrashing Nodes */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-amber-500/40 transition-all hover:shadow-lg hover:shadow-amber-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">Thrashing Nodes</span>
          <div
            className={`p-2 rounded-lg ${
              thrashing.length > 0
                ? 'bg-amber-500/20 text-amber-400 animate-pulse'
                : 'bg-slate-800 text-slate-400'
            } group-hover:scale-110 transition-transform`}
          >
            <Flame className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">{thrashing.length}</div>
        <div className="mt-2 flex items-center justify-between text-xs text-slate-400">
          <span>≥3 edits in 24h</span>
          {thrashing.length > 0 ? (
            <span className="text-[10px] text-amber-400 font-mono font-medium">Fragile Alert</span>
          ) : (
            <span className="text-[10px] text-emerald-400 font-mono">Stable</span>
          )}
        </div>
      </div>

      {/* 5. Spend vs Wasted */}
      <div className="panel bg-gradient-to-b from-slate-900/90 to-slate-950/90 border-white/10 hover:border-rose-500/40 transition-all hover:shadow-lg hover:shadow-rose-500/5 group">
        <div className="flex items-center justify-between">
          <span className="stat-label">AI Dollar Spend</span>
          <div className="p-2 rounded-lg bg-rose-500/15 text-rose-400 group-hover:scale-110 transition-transform">
            <DollarSign className="h-4 w-4" />
          </div>
        </div>
        <div className="mt-3 stat-num text-white">{fmtUSD(totalSpend)}</div>
        <div className="mt-2 flex items-center justify-between text-xs">
          <span className="text-slate-400">Wasted:</span>
          <span className="text-rose-400 font-mono font-medium">{fmtUSD(Math.max(0, wastedSpend))}</span>
        </div>
      </div>

      {/* 6. Top Performing Model */}
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

