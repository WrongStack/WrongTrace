import { useState, useMemo } from 'react';
import {
  BrainCircuit,
  Trophy,
  ShieldCheck,
  Flame,
  DollarSign,
  TrendingUp,
  Award,
  Layers,
  Eye,
  GitCommit,
  Sparkles,
  ArrowUpDown,
  Search,
  Bot,
  Zap,
} from 'lucide-react';
import type { ModelRow, EventRecord, FileReadRecord } from '../types';
import { isJunkModel } from '../types';
import { useRecentReads } from '../hooks/useMetrics';

interface ModelIntelligenceMatrixProps {
  models: ModelRow[];
  events?: EventRecord[];
  loading?: boolean;
  projectId?: string | null;
}

function fmtUSD(n: number): string {
  if (!n || n <= 0) return '$0.0000';
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
}

function getGradeBadge(survivalRate: number, costPerNode: number, blendedCost: number) {
  if (survivalRate >= 85 && (costPerNode <= blendedCost || costPerNode === 0)) {
    return { label: 'S-TIER', color: 'bg-emerald-500/20 text-emerald-300 border-emerald-500/40 shadow-emerald-500/10' };
  }
  if (survivalRate >= 70) {
    return { label: 'A-TIER', color: 'bg-cyan-500/20 text-cyan-300 border-cyan-500/40 shadow-cyan-500/10' };
  }
  if (survivalRate >= 50) {
    return { label: 'B-TIER', color: 'bg-amber-500/20 text-amber-300 border-amber-500/40 shadow-amber-500/10' };
  }
  return { label: 'C-TIER', color: 'bg-rose-500/20 text-rose-300 border-rose-500/40 shadow-rose-500/10' };
}

export function ModelIntelligenceMatrix({
  models = [],
  events = [],
  loading = false,
  projectId = null,
}: ModelIntelligenceMatrixProps) {
  const [activeTab, setActiveTab] = useState<'all' | 'durability' | 'economics' | 'rw_balance'>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [sortBy, setSortBy] = useState<'survival' | 'roi' | 'spend' | 'events'>('survival');
  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('desc');

  const { data: recentReads = [] } = useRecentReads(200, projectId);

  // Map file reads to models
  const readsByModel = useMemo(() => {
    const map = new Map<string, { count: number; tokens: number; cost: number; lines: number }>();
    recentReads.forEach((r) => {
      const m = r.model_name || 'unknown';
      if (!map.has(m)) {
        map.set(m, { count: 0, tokens: 0, cost: 0, lines: 0 });
      }
      const item = map.get(m)!;
      item.count += 1;
      item.tokens += r.prompt_tokens || 0;
      item.cost += r.cost_usd || 0;
      item.lines += r.lines_read_count || 0;
    });
    return map;
  }, [recentReads]);

  // Clean and enrich models
  const enrichedModels = useMemo(() => {
    const valid = models.filter((m) => !isJunkModel(m.model));
    const totalCost = valid.reduce((acc, m) => acc + m.total_cost_usd, 0);
    const totalSurvived = valid.reduce((acc, m) => acc + m.total_survived_nodes, 0);
    const blendedCost = totalSurvived > 0 ? totalCost / totalSurvived : 0.05;

    return valid.map((m) => {
      const reads = readsByModel.get(m.model) || { count: 0, tokens: 0, cost: 0, lines: 0 };
      const wasteSpend =
        m.total_nodes > 0
          ? m.total_cost_usd * Math.max(0, (m.total_nodes - m.active_nodes) / m.total_nodes)
          : 0;

      const productiveSpend = Math.max(0, m.total_cost_usd - wasteSpend);
      const grade = getGradeBadge(m.survival_rate_pct, m.cost_per_surviving_node, blendedCost);

      return {
        ...m,
        readsCount: reads.count,
        readsTokens: reads.tokens,
        readsCost: reads.cost,
        readsLines: reads.lines,
        wasteSpend,
        productiveSpend,
        grade,
      };
    });
  }, [models, readsByModel]);

  // Filter and sort
  const filteredModels = useMemo(() => {
    const q = searchQuery.toLowerCase().trim();
    let list = enrichedModels.filter((m) => {
      if (q && !m.model.toLowerCase().includes(q)) return false;
      return true;
    });

    list.sort((a, b) => {
      let diff = 0;
      if (sortBy === 'survival') diff = b.survival_rate_pct - a.survival_rate_pct;
      else if (sortBy === 'roi') diff = a.cost_per_surviving_node - b.cost_per_surviving_node;
      else if (sortBy === 'spend') diff = b.total_cost_usd - a.total_cost_usd;
      else if (sortBy === 'events') diff = b.total_nodes - a.total_nodes;
      return sortOrder === 'desc' ? diff : -diff;
    });

    return list;
  }, [enrichedModels, searchQuery, sortBy, sortOrder]);

  const toggleSort = (col: 'survival' | 'roi' | 'spend' | 'events') => {
    if (sortBy === col) {
      setSortOrder(sortOrder === 'desc' ? 'asc' : 'desc');
    } else {
      setSortBy(col);
      setSortOrder('desc');
    }
  };

  return (
    <div className="panel space-y-4 bg-gradient-to-b from-slate-900/95 via-slate-950/90 to-slate-900/95 border border-white/10 shadow-2xl rounded-2xl p-5">
      {/* Top Header */}
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-white/10 pb-4">
        <div className="flex items-center gap-3">
          <div className="p-2.5 rounded-xl bg-gradient-to-br from-indigo-500/20 to-purple-500/20 text-indigo-400 border border-indigo-500/30 shadow-inner">
            <BrainCircuit className="h-5 w-5" />
          </div>
          <div>
            <h2 className="font-bold tracking-tight text-base text-white flex items-center gap-2">
              Model Telemetry & Code Durability Matrix
              <span className="text-xs font-mono font-normal text-slate-400">
                ({filteredModels.length} models benchmarked)
              </span>
            </h2>
            <p className="text-xs text-slate-400">
              Correlating Context Reads, AST Code Synthesis, True Token ROI ($/survived node), and Survival Rates.
            </p>
          </div>
        </div>

        {/* Search & Tabs */}
        <div className="flex flex-wrap items-center gap-2.5">
          <div className="relative">
            <Search className="h-3.5 w-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
            <input
              type="text"
              placeholder="Search model…"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-8 pr-3 py-1.5 text-xs bg-slate-950 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent w-44 sm:w-56"
            />
          </div>

          <div className="flex items-center bg-slate-950 border border-white/10 rounded-lg p-0.5 text-xs">
            {(
              [
                { id: 'all', label: 'Overview' },
                { id: 'durability', label: 'Durability' },
                { id: 'economics', label: 'Token ROI' },
                { id: 'rw_balance', label: 'Read vs Write' },
              ] as const
            ).map((t) => (
              <button
                key={t.id}
                onClick={() => setActiveTab(t.id)}
                className={`px-2.5 py-1 rounded-md font-medium transition-all ${
                  activeTab === t.id
                    ? 'bg-accent text-white shadow-sm font-semibold'
                    : 'text-slate-400 hover:text-slate-200'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Main Table */}
      {loading ? (
        <div className="py-12 text-center text-xs text-slate-500 animate-pulse">
          Synthesizing model telemetry & code survival statistics…
        </div>
      ) : filteredModels.length === 0 ? (
        <div className="py-12 text-center text-xs text-slate-500">
          No model telemetry records found matching the query.
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs border-collapse">
            <thead>
              <tr className="border-b border-white/10 text-[11px] font-mono text-slate-400 uppercase tracking-wider">
                <th className="py-2.5 px-3">Model</th>
                <th className="py-2.5 px-3">Quality Tier</th>
                <th
                  onClick={() => toggleSort('survival')}
                  className="py-2.5 px-3 text-right cursor-pointer hover:text-white transition-colors"
                >
                  <span className="inline-flex items-center gap-1">
                    Survival % <ArrowUpDown className="h-3 w-3 text-slate-500" />
                  </span>
                </th>
                <th className="py-2.5 px-3 text-right">Active / Total</th>
                <th
                  onClick={() => toggleSort('roi')}
                  className="py-2.5 px-3 text-right cursor-pointer hover:text-white transition-colors"
                >
                  <span className="inline-flex items-center gap-1">
                    True ROI ($/Node) <ArrowUpDown className="h-3 w-3 text-slate-500" />
                  </span>
                </th>
                <th className="py-2.5 px-3 text-right">Avg Longevity</th>
                <th
                  onClick={() => toggleSort('spend')}
                  className="py-2.5 px-3 text-right cursor-pointer hover:text-white transition-colors"
                >
                  <span className="inline-flex items-center gap-1">
                    Total Spend <ArrowUpDown className="h-3 w-3 text-slate-500" />
                  </span>
                </th>
                <th className="py-2.5 px-3 text-right text-cyan-400">Context Reads</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-white/5 font-mono">
              {filteredModels.map((m) => {
                const isHighSurvival = m.survival_rate_pct >= 75;
                const isMedSurvival = m.survival_rate_pct >= 50 && m.survival_rate_pct < 75;

                return (
                  <tr key={m.model} className="hover:bg-white/[0.03] transition-colors group">
                    {/* Model Name */}
                    <td className="py-3 px-3">
                      <div className="flex items-center gap-2">
                        <div className="p-1 rounded bg-slate-900 border border-white/5 text-slate-300">
                          <Bot className="h-3.5 w-3.5 text-accent" />
                        </div>
                        <div>
                          <div className="font-bold text-slate-100 group-hover:text-cyan-300 transition-colors">
                            {m.model}
                          </div>
                          <div className="text-[10px] text-slate-500 font-sans">{m.run_count} agent sessions</div>
                        </div>
                      </div>
                    </td>

                    {/* Grade Tier */}
                    <td className="py-3 px-3">
                      <span className={`chip text-[10px] px-2 py-0.5 rounded-full font-bold border ${m.grade.color}`}>
                        {m.grade.label}
                      </span>
                    </td>

                    {/* Survival Rate */}
                    <td className="py-3 px-3 text-right">
                      <div className="flex flex-col items-end gap-1">
                        <span
                          className={`font-bold text-sm ${
                            isHighSurvival ? 'text-emerald-400' : isMedSurvival ? 'text-amber-400' : 'text-rose-400'
                          }`}
                        >
                          {m.survival_rate_pct.toFixed(1)}%
                        </span>
                        <div className="w-20 bg-slate-950 h-1.5 rounded-full overflow-hidden border border-white/10">
                          <div
                            className={`h-full rounded-full ${
                              isHighSurvival ? 'bg-emerald-400' : isMedSurvival ? 'bg-amber-400' : 'bg-rose-400'
                            }`}
                            style={{ width: `${Math.min(100, Math.max(0, m.survival_rate_pct))}%` }}
                          />
                        </div>
                      </div>
                    </td>

                    {/* Active / Total Nodes */}
                    <td className="py-3 px-3 text-right">
                      <span className="text-slate-200 font-semibold">{m.active_nodes}</span>
                      <span className="text-slate-500"> / {m.total_nodes}</span>
                    </td>

                    {/* True Token ROI */}
                    <td className="py-3 px-3 text-right">
                      {m.total_survived_nodes > 0 ? (
                        <span className="text-emerald-400 font-bold">
                          ${m.cost_per_surviving_node.toFixed(4)}
                        </span>
                      ) : (
                        <span className="text-slate-500">—</span>
                      )}
                    </td>

                    {/* Avg Longevity */}
                    <td className="py-3 px-3 text-right text-slate-300">
                      {m.avg_longevity_days > 0 ? `${m.avg_longevity_days.toFixed(1)} days` : 'New'}
                    </td>

                    {/* Total Spend & Waste Breakdown */}
                    <td className="py-3 px-3 text-right">
                      <div className="font-bold text-slate-100">{fmtUSD(m.total_cost_usd)}</div>
                      {m.wasteSpend > 0.001 && (
                        <div className="text-[10px] text-rose-400">
                          ({fmtUSD(m.wasteSpend)} churn waste)
                        </div>
                      )}
                    </td>

                    {/* Context Reads */}
                    <td className="py-3 px-3 text-right">
                      {m.readsCount > 0 ? (
                        <div>
                          <span className="text-cyan-300 font-bold">{m.readsCount}x</span>
                          <span className="text-slate-500 text-[10px]"> ({(m.readsTokens / 1000).toFixed(0)}k tok)</span>
                        </div>
                      ) : (
                        <span className="text-slate-600">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
