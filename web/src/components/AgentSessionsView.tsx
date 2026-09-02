import { useState, useMemo } from 'react';
import {
  Bot,
  Cpu,
  Activity,
  DollarSign,
  Clock,
  Sparkles,
  Zap,
  CheckCircle2,
  AlertTriangle,
  Search,
  KeyRound,
  Layers,
  Terminal,
  Plus,
  Coins,
  Calculator,
  Sliders,
  Check,
  Code2,
  RefreshCw,
  Copy,
  TrendingDown,
  Globe,
  ShieldCheck,
  Server,
  ExternalLink,
  ChevronDown,
  ChevronUp,
} from 'lucide-react';
import { RichDiffViewer } from './RichDiffViewer';
import type { ActiveRun, ModelRow, Overview, ModelInfo, ProviderInfo, EventRecord, Project } from '../types';
import { isJunkModel, formatCleanModel } from '../types';
import { useModelCatalog, useProviderCatalog } from '../hooks/useMetrics';

interface AgentSessionsViewProps {
  activeRuns: ActiveRun[];
  models: ModelRow[];
  overview?: Overview;
  recentEvents?: EventRecord[];
  loading: boolean;
  currentProject?: Project | null;
}

export function AgentSessionsView({
  activeRuns,
  models,
  overview,
  recentEvents = [],
  loading,
  currentProject,
}: AgentSessionsViewProps) {
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  const [activeSubTab, setActiveSubTab] = useState<'sessions' | 'catalog' | 'calculator'>('catalog');
  const [search, setSearch] = useState('');
  const [catalogViewMode, setCatalogViewMode] = useState<'providers' | 'compare' | 'flat'>('providers');
  const [expandedProviderId, setExpandedProviderId] = useState<string | null>(null);
  const [selectedProviderFilter, setSelectedProviderFilter] = useState<string>('all');
  const [copiedModelId, setCopiedModelId] = useState<string | null>(null);

  // Model & Provider Catalog Queries
  const catalogQuery = useModelCatalog();
  const providerCatalogQuery = useProviderCatalog();
  const catalogList: ModelInfo[] = catalogQuery.data ?? [];
  const providersList: ProviderInfo[] = providerCatalogQuery.data ?? [];
  const [isSyncing, setIsSyncing] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);

  const handleCopyModel = (text: string) => {
    navigator.clipboard.writeText(text);
    setCopiedModelId(text);
    setTimeout(() => setCopiedModelId(null), 2000);
  };

  const handleSyncModels = async () => {
    setIsSyncing(true);
    setSyncError(null);
    try {
      const res = await fetch('/api/models/sync', { method: 'POST' });
      if (!res.ok) {
        throw new Error(`Sync failed with status: ${res.status}`);
      }
      catalogQuery.refetch();
      providerCatalogQuery.refetch();
    } catch (err: any) {
      setSyncError(err.message || 'Sync failed');
    } finally {
      setIsSyncing(false);
    }
  };

  // Cost Calculator State
  const [calcModel, setCalcModel] = useState<string>('');
  const [promptTokens, setPromptTokens] = useState<number>(100_000);
  const [completionTokens, setCompletionTokens] = useState<number>(20_000);

  // Custom Model Form State
  const [showAddModal, setShowAddModal] = useState(false);
  const [customId, setCustomId] = useState('');
  const [customName, setCustomName] = useState('');
  const [customProvider, setCustomProvider] = useState('Local');
  const [customInPrice, setCustomInPrice] = useState('0.50');
  const [customOutPrice, setCustomOutPrice] = useState('2.00');
  const [customCtx, setCustomCtx] = useState('128000');
  const [isSavingModel, setIsSavingModel] = useState(false);
  const [saveSuccess, setSaveSuccess] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const filteredRuns = useMemo(() => {
    const q = search.toLowerCase().trim();
    return activeRuns.filter((r) => {
      if (isJunkModel(r.model_name)) return false;
      if (currentProject) {
        if (r.project_id && r.project_id !== currentProject.id) return false;
        if (r.project_slug && !r.project_slug.toLowerCase().includes(currentProject.name.toLowerCase()) && !currentProject.name.toLowerCase().includes(r.project_slug.toLowerCase())) {
          return false;
        }
      }
      if (q && !r.agent_name.toLowerCase().includes(q) && !r.model_name.toLowerCase().includes(q) && !r.run_id.toLowerCase().includes(q)) {
        return false;
      }
      return true;
    });
  }, [activeRuns, search, currentProject]);

  const selectedRun = useMemo(() => {
    if (selectedRunId) {
      const found = activeRuns.find((r) => r.run_id === selectedRunId);
      if (found) return found;
    }
    return activeRuns[0] ?? null;
  }, [selectedRunId, activeRuns]);

  // Extract all unique providers with counts
  const allProviders = useMemo(() => {
    if (providersList.length > 0) {
      return providersList.map((p) => ({ name: p.name, count: p.model_count })).sort((a, b) => b.count - a.count);
    }
    const counts = new Map<string, number>();
    catalogList.forEach((m) => {
      if (isJunkModel(m.model_id) || isJunkModel(m.id)) return;
      const p = m.provider || 'Custom';
      counts.set(p, (counts.get(p) || 0) + 1);
    });
    return Array.from(counts.entries())
      .map(([name, count]) => ({ name, count }))
      .sort((a, b) => b.count - a.count);
  }, [providersList, catalogList]);

  // Filtered Providers Catalog
  const filteredProvidersList = useMemo(() => {
    const q = search.toLowerCase().trim();
    return providersList.filter((p) => {
      if (selectedProviderFilter !== 'all' && p.name.toLowerCase() !== selectedProviderFilter.toLowerCase() && p.id.toLowerCase() !== selectedProviderFilter.toLowerCase()) {
        return false;
      }
      if (!q) return true;
      return (
        p.name.toLowerCase().includes(q) ||
        p.id.toLowerCase().includes(q) ||
        (p.api && p.api.toLowerCase().includes(q)) ||
        (p.npm && p.npm.toLowerCase().includes(q)) ||
        (p.models && p.models.some((m) => !isJunkModel(m.id) && (m.name.toLowerCase().includes(q) || m.id.toLowerCase().includes(q))))
      );
    });
  }, [providersList, search, selectedProviderFilter]);

  // Group catalog by canonical model name / bare model ID
  const groupedCatalog = useMemo(() => {
    const groups = new Map<string, {
      modelId: string;
      name: string;
      description: string;
      contextWindow: number;
      isCustom: boolean;
      canonicalProvider: string;
      providers: Array<{
        provider: string;
        providerId: string;
        providerApi?: string;
        inputPrice: number;
        outputPrice: number;
        cacheReadPrice: number;
        contextWindow: number;
        isCanonical: boolean;
        fullId: string;
      }>;
      lowestInputPrice: number;
      lowestOutputPrice: number;
      bestProvider: string;
    }>();

    catalogList.forEach((m) => {
      const bareId = m.model_id || (m.id.includes('/') ? m.id.split('/')[1] : m.id);
      if (isJunkModel(bareId) || isJunkModel(m.id)) {
        return;
      }
      const groupKey = bareId.toLowerCase();

      if (!groups.has(groupKey)) {
        groups.set(groupKey, {
          modelId: bareId,
          name: m.name || bareId,
          description: m.description,
          contextWindow: m.context_window,
          isCustom: !!m.is_custom,
          canonicalProvider: m.provider,
          providers: [],
          lowestInputPrice: m.input_price_per_m,
          lowestOutputPrice: m.output_price_per_m,
          bestProvider: m.provider,
        });
      }

      const g = groups.get(groupKey)!;
      if (m.description && !g.description) {
        g.description = m.description;
      }
      if (m.context_window > g.contextWindow) {
        g.contextWindow = m.context_window;
      }
      if (m.is_canonical) {
        g.canonicalProvider = m.provider;
        g.name = m.name;
      }

      const existing = g.providers.find((p) => p.provider.toLowerCase() === m.provider.toLowerCase());
      if (!existing) {
        g.providers.push({
          provider: m.provider,
          providerId: m.provider_id || m.provider.toLowerCase(),
          providerApi: m.provider_api,
          inputPrice: m.input_price_per_m,
          outputPrice: m.output_price_per_m,
          cacheReadPrice: m.cache_read_price_per_m,
          contextWindow: m.context_window,
          isCanonical: !!m.is_canonical,
          fullId: m.id,
        });
      }

      if (m.input_price_per_m > 0 && (g.lowestInputPrice === 0 || m.input_price_per_m < g.lowestInputPrice)) {
        g.lowestInputPrice = m.input_price_per_m;
        g.bestProvider = m.provider;
      }
      if (m.output_price_per_m > 0 && (g.lowestOutputPrice === 0 || m.output_price_per_m < g.lowestOutputPrice)) {
        g.lowestOutputPrice = m.output_price_per_m;
      }
    });

    return Array.from(groups.values()).sort((a, b) => {
      if (a.providers.length !== b.providers.length) {
        return b.providers.length - a.providers.length;
      }
      return a.name.localeCompare(b.name);
    });
  }, [catalogList]);

  // Filtered Grouped Catalog
  const filteredGroupedCatalog = useMemo(() => {
    const q = search.toLowerCase().trim();
    return groupedCatalog.filter((g) => {
      if (selectedProviderFilter !== 'all') {
        const hasProv = g.providers.some((p) => p.provider.toLowerCase() === selectedProviderFilter.toLowerCase());
        if (!hasProv) return false;
      }
      if (!q) return true;
      return (
        g.name.toLowerCase().includes(q) ||
        g.modelId.toLowerCase().includes(q) ||
        g.description.toLowerCase().includes(q) ||
        g.providers.some((p) => p.provider.toLowerCase().includes(q))
      );
    });
  }, [groupedCatalog, search, selectedProviderFilter]);

  // Filtered Flat Catalog
  const filteredCatalog = useMemo(() => {
    const q = search.toLowerCase().trim();
    return catalogList.filter((m) => {
      if (selectedProviderFilter !== 'all' && m.provider.toLowerCase() !== selectedProviderFilter.toLowerCase()) {
        return false;
      }
      if (!q) return true;
      return (
        m.name.toLowerCase().includes(q) ||
        m.id.toLowerCase().includes(q) ||
        m.provider.toLowerCase().includes(q) ||
        (m.model_id && m.model_id.toLowerCase().includes(q))
      );
    });
  }, [catalogList, search, selectedProviderFilter]);

  // Calculator Result
  const calculatedCost = useMemo(() => {
    const targetModel = calcModel || catalogList[0]?.id;
    const selected = catalogList.find((m) => m.id === targetModel || m.model_id === targetModel);
    if (!selected) {
      return (promptTokens * 2.0 / 1e6) + (completionTokens * 8.0 / 1e6);
    }
    const inCost = (promptTokens * selected.input_price_per_m) / 1e6;
    const outCost = (completionTokens * selected.output_price_per_m) / 1e6;
    return inCost + outCost;
  }, [catalogList, calcModel, promptTokens, completionTokens]);

  const handleSaveCustomModel = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!customId) return;
    setIsSavingModel(true);
    try {
      const res = await fetch('/api/models/catalog', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: customId,
          name: customName || customId,
          provider: customProvider || 'Custom',
          input_price_per_m: parseFloat(customInPrice) || 0,
          output_price_per_m: parseFloat(customOutPrice) || 0,
          context_window: parseInt(customCtx, 10) || 128000,
          description: 'Custom registered AI model',
        }),
      });
      const data = await res.json().catch(() => null);
      if (!res.ok) {
        // The API answers {"error": "..."} for duplicate ids and invalid
        // bodies; without this branch the modal silently stayed open.
        setSaveError(data?.error || `Failed to save model (${res.status})`);
        return;
      }
      setSaveSuccess(true);
      catalogQuery.refetch();
      setTimeout(() => {
        setSaveSuccess(false);
        setShowAddModal(false);
        setCustomId('');
        setCustomName('');
      }, 1200);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save model');
    } finally {
      setIsSavingModel(false);
    }
  };

  return (
    <div className="space-y-4">
      {/* Top Header & Sub-Tabs */}
      <div className="panel flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-lg bg-accent/20 text-accent">
            <Bot className="h-5 w-5" />
          </div>
          <div>
            <h2 className="font-semibold tracking-tight text-base flex items-center gap-2">
              Agent Telemetry & Model Intelligence
              <span className="text-xs font-normal text-slate-400 flex items-center gap-1.5">
                · {filteredRuns.length} active sessions · {catalogList.length} catalog models
                {currentProject && (
                  <span className="text-[11px] text-cyan-400 font-mono bg-cyan-500/10 px-2 py-0.5 rounded-full border border-cyan-500/20">
                    {currentProject.name}
                  </span>
                )}
              </span>
            </h2>
            <p className="text-xs text-slate-400">
              Live IPC / MCP sessions, up-to-date 2025/2026 model pricing registry, and real-time ROI telemetry.
            </p>
          </div>
        </div>

        {/* Sub-Tab Navigation */}
        <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-1 text-xs">
          <button
            onClick={() => setActiveSubTab('sessions')}
            className={`px-3 py-1.5 rounded-md font-medium transition-all ${
              activeSubTab === 'sessions' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            Active Sessions ({filteredRuns.length})
          </button>
          <button
            onClick={() => setActiveSubTab('catalog')}
            className={`px-3 py-1.5 rounded-md font-medium transition-all ${
              activeSubTab === 'catalog' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            Model Pricing Catalog ({catalogList.length})
          </button>
          <button
            onClick={() => setActiveSubTab('calculator')}
            className={`px-3 py-1.5 rounded-md font-medium transition-all ${
              activeSubTab === 'calculator' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            Cost Simulator
          </button>
        </div>
      </div>

      {/* ------------------------------------------------------------- */}
      {/* 1. SESSIONS TAB */}
      {/* ------------------------------------------------------------- */}
      {activeSubTab === 'sessions' && (
        <>
          {/* Model Cards Overview */}
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
            {models.map((m) => (
              <div key={m.model} className="panel hover:border-accent/30 transition-all space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <Cpu className="h-4 w-4 text-cyan-400" />
                    <span className="font-mono text-xs font-semibold text-white truncate max-w-[140px]">{m.model}</span>
                  </div>
                  <span className="chip bg-accent/15 text-accent text-[10px] font-mono">
                    {m.run_count} runs
                  </span>
                </div>

                <div className="grid grid-cols-2 gap-2 text-xs">
                  <div className="panel-raised p-2">
                    <div className="text-[10px] text-slate-400">Survival Rate</div>
                    <div className="font-bold text-sm text-emerald-400 mt-0.5">{m.survival_rate_pct.toFixed(1)}%</div>
                  </div>
                  <div className="panel-raised p-2">
                    <div className="text-[10px] text-slate-400">Total Spend</div>
                    <div className="font-bold text-sm text-slate-200 mt-0.5">${m.total_cost_usd.toFixed(2)}</div>
                  </div>
                </div>

                <div className="text-[11px] text-slate-400 flex items-center justify-between border-t border-white/5 pt-2 font-mono">
                  <span>{m.total_survived_nodes} / {m.total_nodes} nodes alive</span>
                  <span className="text-accent">${m.cost_per_surviving_node.toFixed(4)} / node</span>
                </div>
              </div>
            ))}
          </div>

          {/* Sessions Layout */}
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-4">
            {/* Left: Active Runs List */}
            <div className="lg:col-span-6 panel p-0 overflow-hidden flex flex-col h-[520px]">
              <div className="p-3 bg-white/5 border-b border-white/5 flex items-center justify-between text-xs text-slate-400 font-medium">
                <span>Connected Agent Sessions</span>
                <span>{filteredRuns.length} active</span>
              </div>

              <div className="divide-y divide-white/5 overflow-y-auto flex-1 p-2 space-y-1">
                {loading && <div className="p-4 text-xs text-slate-500">Loading session stream…</div>}

                {!loading && filteredRuns.length === 0 && (
                  <div className="p-8 text-center text-xs text-slate-500">
                    No active agent sessions currently connected.
                    <div className="mt-2 text-[11px] text-slate-600 font-mono">
                      Agents connect automatically via Named Pipe or MCP tool calls.
                    </div>
                  </div>
                )}

                {filteredRuns.map((r) => {
                  const isSelected = selectedRun?.run_id === r.run_id;
                  return (
                    <div
                      key={r.run_id}
                      onClick={() => setSelectedRunId(r.run_id)}
                      className={`p-3 rounded-lg border transition-all cursor-pointer text-xs space-y-1.5 ${
                        isSelected
                          ? 'bg-accent/15 border-accent/40 shadow-sm'
                          : 'bg-slate-900/50 border-transparent hover:border-white/10 hover:bg-white/[0.03]'
                      }`}
                    >
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2">
                          <span className="h-2 w-2 rounded-full bg-emerald-400 animate-ping" />
                          <span className="font-semibold text-white">{r.agent_name}</span>
                        </div>
                        <span className="font-mono text-[10px] text-accent bg-accent/10 px-2 py-0.5 rounded border border-accent/20">
                          {formatCleanModel(r.model_name, r.agent_name)}
                        </span>
                      </div>

                      <div className="font-mono text-[11px] text-slate-400 truncate">
                        Run: {r.run_id}
                      </div>

                      <div className="flex items-center justify-between text-[10px] text-slate-500 font-mono">
                        <span>Task: {r.task_id}</span>
                        <span>Started: {new Date(r.started_at).toLocaleTimeString()}</span>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>

            {/* Right: Selected Session Telemetry Details */}
            <div className="lg:col-span-6 panel space-y-4">
              <div className="flex items-center justify-between border-b border-white/5 pb-3">
                <div className="flex items-center gap-2">
                  <Sparkles className="h-4 w-4 text-accent" />
                  <h3 className="font-semibold text-sm">Session Details</h3>
                </div>
                {selectedRun && (
                  <span className="chip bg-emerald-500/15 text-emerald-400 border border-emerald-500/30 text-[10px]">
                    In-Flight Live
                  </span>
                )}
              </div>

              {selectedRun ? (
                <div className="space-y-3 text-xs">
                  <div>
                    <div className="text-slate-400 mb-1">Agent Run Identifier</div>
                    <div className="font-mono text-slate-200 bg-slate-950 p-2 rounded-lg border border-white/5 break-all">
                      {selectedRun.run_id}
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-2">
                    <div className="panel-raised p-2.5">
                      <div className="text-slate-400 text-[11px]">Agent Client</div>
                      <div className="font-semibold text-white mt-0.5">{selectedRun.agent_name}</div>
                    </div>
                    <div className="panel-raised p-2.5">
                      <div className="text-slate-400 text-[11px]">AI Model</div>
                      <div className="font-mono text-accent font-semibold mt-0.5">
                        {formatCleanModel(selectedRun.model_name, selectedRun.agent_name)}
                      </div>
                    </div>
                    <div className="panel-raised p-2.5">
                      <div className="text-slate-400 text-[11px]">Task Reference</div>
                      <div className="font-mono text-slate-300 mt-0.5 truncate">{selectedRun.task_id}</div>
                    </div>
                    <div className="panel-raised p-2.5">
                      <div className="text-slate-400 text-[11px]">Session Started</div>
                      <div className="font-mono text-slate-300 mt-0.5">
                        {new Date(selectedRun.started_at).toLocaleTimeString()}
                      </div>
                    </div>
                  </div>

                  {/* Session Code Mutations & Diffs */}
                  {(() => {
                    const sessionDiffs = recentEvents.filter(
                      (e) => (e.run_id === selectedRun.run_id || selectedRun.agent_name === 'WrongStack') && !!e.diff_snippet
                    );
                    if (sessionDiffs.length === 0) return null;
                    return (
                      <div className="space-y-2 pt-2 border-t border-white/5">
                        <div className="flex items-center justify-between text-xs text-slate-400">
                          <span className="font-semibold flex items-center gap-1.5 text-slate-300">
                            <Code2 className="h-3.5 w-3.5 text-accent" />
                            Code Changes Produced in this Session ({sessionDiffs.length})
                          </span>
                        </div>
                        <div className="space-y-2">
                          {sessionDiffs.slice(0, 5).map((diffEv) => (
                            <div key={diffEv.event_id} className="space-y-1">
                              <div className="text-[11px] font-mono text-cyan-300 truncate">
                                {diffEv.file_path} :: {diffEv.node_signature}
                              </div>
                              <RichDiffViewer
                                diff={diffEv.diff_snippet}
                                filePath={diffEv.file_path}
                                signature={diffEv.node_signature}
                                action={diffEv.action}
                                startLine={diffEv.start_line}
                                endLine={diffEv.end_line}
                                maxHeight="180px"
                              />
                            </div>
                          ))}
                        </div>
                      </div>
                    );
                  })()}

                  <div className="p-3 rounded-lg bg-indigo-500/10 border border-indigo-500/20 text-indigo-200 text-[11px] space-y-1">
                    <div className="font-semibold flex items-center gap-1.5">
                      <Zap className="h-3.5 w-3.5 text-accent" />
                      IPC Health & Guardrail Status
                    </div>
                    <p className="text-slate-300 leading-relaxed">
                      WrongTrace automatically tracks token pricing and correlates AST transitions with the active model catalog.
                    </p>
                  </div>
                </div>
              ) : (
                <div className="flex flex-col items-center justify-center h-48 text-slate-500 text-sm gap-2">
                  <Bot className="h-8 w-8 text-slate-600" />
                  <span>Select an active session to view telemetry.</span>
                </div>
              )}
            </div>
          </div>
        </>
      )}

      {/* ------------------------------------------------------------- */}
      {/* 2. MODEL PRICING CATALOG TAB */}
      {/* ------------------------------------------------------------- */}
      {activeSubTab === 'catalog' && (
        <div className="space-y-4">
          {/* Search, View Mode, and Sync Controls */}
          <div className="flex flex-col lg:flex-row items-start lg:items-center justify-between gap-3 bg-slate-900/60 p-3 rounded-xl border border-white/5">
            <div className="flex flex-wrap items-center gap-2 flex-1 max-w-2xl">
              <div className="relative flex-1 min-w-[200px] max-w-xs">
                <Search className="h-3.5 w-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
                <input
                  type="text"
                  placeholder="Search models, providers, features…"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  className="w-full pl-8 pr-3 py-1.5 text-xs bg-slate-950 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent"
                />
              </div>

              {/* View Mode Toggle */}
              <div className="flex items-center bg-slate-950 border border-white/10 rounded-lg p-0.5 text-xs">
                <button
                  onClick={() => setCatalogViewMode('providers')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded transition-all font-medium ${
                    catalogViewMode === 'providers'
                      ? 'bg-accent text-white shadow-sm'
                      : 'text-slate-400 hover:text-slate-200'
                  }`}
                >
                  <Server className="h-3 w-3" />
                  <span>AI Providers</span>
                  <span className="text-[10px] px-1 py-0.2 rounded bg-white/20 font-mono">
                    {filteredProvidersList.length}
                  </span>
                </button>
                <button
                  onClick={() => setCatalogViewMode('compare')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded transition-all font-medium ${
                    catalogViewMode === 'compare'
                      ? 'bg-accent text-white shadow-sm'
                      : 'text-slate-400 hover:text-slate-200'
                  }`}
                >
                  <Layers className="h-3 w-3" />
                  <span>Model Comparison</span>
                  <span className="text-[10px] px-1 py-0.2 rounded bg-white/20 font-mono">
                    {filteredGroupedCatalog.length}
                  </span>
                </button>
                <button
                  onClick={() => setCatalogViewMode('flat')}
                  className={`flex items-center gap-1.5 px-2.5 py-1 rounded transition-all font-medium ${
                    catalogViewMode === 'flat'
                      ? 'bg-accent text-white shadow-sm'
                      : 'text-slate-400 hover:text-slate-200'
                  }`}
                >
                  <Globe className="h-3 w-3" />
                  <span>All Provider Variants</span>
                  <span className="text-[10px] px-1 py-0.2 rounded bg-white/20 font-mono">
                    {filteredCatalog.length}
                  </span>
                </button>
              </div>
            </div>

            <div className="flex items-center gap-2 flex-wrap">
              {syncError && (
                <span className="text-[11px] text-red-400 font-mono truncate max-w-xs" title={syncError}>
                  {syncError}
                </span>
              )}
              <button
                onClick={handleSyncModels}
                disabled={isSyncing}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs bg-slate-950 border border-white/10 text-slate-200 hover:border-accent/50 rounded-lg transition-all font-medium disabled:opacity-50"
              >
                <RefreshCw className={`h-3.5 w-3.5 ${isSyncing ? 'animate-spin' : ''}`} />
                {isSyncing ? 'Syncing…' : 'Sync models.dev'}
              </button>

              <button
                onClick={() => setShowAddModal(true)}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs bg-accent hover:bg-accent/90 text-white rounded-lg transition-all shadow-md shadow-accent/20 font-medium"
              >
                <Plus className="h-3.5 w-3.5" />
                Add Custom
              </button>
            </div>
          </div>

          {/* Provider Filter Chips Bar */}
          <div className="flex items-center gap-1.5 overflow-x-auto pb-1 text-xs font-mono scrollbar-thin">
            <span className="text-slate-500 text-[11px] uppercase tracking-wider shrink-0 pr-1">Provider:</span>
            {['all', 'Anthropic', 'OpenAI', 'DeepSeek', 'Google', 'MiniMax', 'Moonshot', 'Inflection', 'Z.ai', 'Groq', 'OpenRouter', 'Mistral', 'Together AI', 'Meta', 'Alibaba Cloud', 'xAI', 'DeepInfra', 'Cerebras', 'Fireworks'].map((prov) => {
              const isSelected = selectedProviderFilter.toLowerCase() === prov.toLowerCase();
              return (
                <button
                  key={prov}
                  onClick={() => setSelectedProviderFilter(prov.toLowerCase())}
                  className={`px-2.5 py-1 rounded-lg border text-[11px] whitespace-nowrap transition-all ${
                    isSelected
                      ? 'bg-accent/20 border-accent text-accent font-bold shadow-sm'
                      : 'bg-slate-900/70 border-white/5 text-slate-400 hover:text-slate-200 hover:border-white/20'
                  }`}
                >
                  {prov === 'all' ? 'All Providers' : prov}
                </button>
              );
            })}

            {/* Provider Dropdown for full list */}
            {allProviders.length > 0 && (
              <select
                value={selectedProviderFilter}
                onChange={(e) => setSelectedProviderFilter(e.target.value)}
                aria-label="Filter by provider"
                className="bg-slate-900 text-slate-300 text-[11px] px-2 py-1 rounded-lg border border-white/10 focus:outline-none focus:border-accent ml-1"
              >
                <option value="all">More Providers ({allProviders.length})</option>
                {allProviders.map((p) => (
                  <option key={p.name} value={p.name.toLowerCase()}>
                    {p.name} ({p.count})
                  </option>
                ))}
              </select>
            )}
          </div>

          {catalogQuery.isLoading && (
            <div className="panel p-8 text-center text-xs text-slate-500">Loading model catalog…</div>
          )}

          {!catalogQuery.isLoading && (
            catalogViewMode === 'providers'
              ? filteredProvidersList.length === 0
              : catalogViewMode === 'compare'
              ? filteredGroupedCatalog.length === 0
              : filteredCatalog.length === 0
          ) && (
            <div className="panel p-8 text-center text-xs text-slate-500 space-y-2">
              <div>No providers or models matching your query or provider filter.</div>
              <div className="text-[11px] text-slate-600">
                Try clearing the search filter or resetting the provider selector to <span className="font-mono text-accent">All Providers</span>.
              </div>
              <button
                onClick={() => {
                  setSearch('');
                  setSelectedProviderFilter('all');
                }}
                className="mt-1 px-3 py-1.5 text-xs bg-slate-800 hover:bg-slate-700 text-slate-300 rounded-lg font-medium"
              >
                Reset Filters
              </button>
            </div>
          )}

          {/* VIEW MODE 1: AI PROVIDERS DIRECTORY */}
          {catalogViewMode === 'providers' && (
            <div className="space-y-3">
              {filteredProvidersList.map((p) => {
                const isExpanded = expandedProviderId === p.id;
                return (
                  <div
                    key={p.id}
                    className="panel border border-white/5 bg-slate-900/70 hover:border-accent/30 transition-all space-y-3"
                  >
                    {/* Provider Header Bar */}
                    <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                      <div className="flex items-center gap-3">
                        <div className="p-2.5 rounded-xl bg-slate-950 border border-white/10 text-accent shrink-0">
                          <Server className="h-5 w-5" />
                        </div>
                        <div>
                          <div className="flex items-center gap-2">
                            <h3 className="font-bold text-white text-sm">{p.name}</h3>
                            <span className="font-mono text-[10px] text-slate-400 bg-slate-950 px-2 py-0.5 rounded border border-white/5">
                              {p.id}
                            </span>
                            <span className="chip bg-accent/15 text-accent border border-accent/20 text-[10px] font-mono">
                              {p.model_count} {p.model_count === 1 ? 'model' : 'models'}
                            </span>
                          </div>
                          <div className="flex items-center gap-4 mt-1 text-xs text-slate-400 font-mono flex-wrap">
                            {p.api && (
                              <span className="flex items-center gap-1 truncate max-w-xs" title={p.api}>
                                <span className="text-slate-500">API:</span> {p.api}
                              </span>
                            )}
                            {p.npm && (
                              <span className="flex items-center gap-1 truncate max-w-xs" title={p.npm}>
                                <span className="text-slate-500">SDK:</span> {p.npm}
                              </span>
                            )}
                          </div>
                        </div>
                      </div>

                      <div className="flex items-center gap-2 shrink-0">
                        {p.doc && (
                          <a
                            href={p.doc}
                            target="_blank"
                            rel="noreferrer"
                            className="flex items-center gap-1 px-2.5 py-1 text-xs bg-slate-950 border border-white/10 text-slate-300 hover:text-white hover:border-white/20 rounded-lg transition-colors"
                          >
                            <span>Docs</span>
                            <ExternalLink className="h-3 w-3" />
                          </a>
                        )}
                        <button
                          onClick={() => setExpandedProviderId(isExpanded ? null : p.id)}
                          className={`flex items-center gap-1.5 px-3 py-1 text-xs rounded-lg font-medium transition-all ${
                            isExpanded
                              ? 'bg-accent text-white shadow-sm'
                              : 'bg-slate-950 border border-white/10 text-slate-300 hover:text-white hover:border-accent/40'
                          }`}
                        >
                          <span>{isExpanded ? 'Hide Models' : `Explore Models (${p.model_count})`}</span>
                          {isExpanded ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
                        </button>
                      </div>
                    </div>

                    {/* Expandable Provider Model List */}
                    {isExpanded && (
                      <div className="pt-3 border-t border-white/5 space-y-2">
                        <div className="text-[10px] font-mono text-slate-400 uppercase tracking-wider">
                          Hosted Models under {p.name} ({p.models?.length || 0})
                        </div>

                        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
                          {(p.models || []).map((m) => (
                            <div
                              key={m.id}
                              className="p-3 rounded-xl border border-white/5 bg-slate-950/80 space-y-2 flex flex-col justify-between"
                            >
                              <div className="space-y-1.5">
                                <div className="flex items-start justify-between gap-2">
                                  <div className="min-w-0">
                                    <div className="font-semibold text-white text-xs truncate" title={m.name}>
                                      {m.name}
                                    </div>
                                    <div className="flex items-center gap-1.5 mt-0.5">
                                      <span className="font-mono text-[10px] text-accent truncate" title={m.id}>
                                        {m.id}
                                      </span>
                                      <button
                                        onClick={() => handleCopyModel(m.id)}
                                        className="text-slate-500 hover:text-slate-300 p-0.5"
                                        title="Copy Model ID"
                                      >
                                        {copiedModelId === m.id ? (
                                          <Check className="h-2.5 w-2.5 text-emerald-400" />
                                        ) : (
                                          <Copy className="h-2.5 w-2.5" />
                                        )}
                                      </button>
                                    </div>
                                  </div>

                                  {m.context_window > 0 && (
                                    <span className="chip bg-white/5 text-slate-400 text-[9px] font-mono shrink-0">
                                      {(m.context_window / 1000).toFixed(0)}k ctx
                                    </span>
                                  )}
                                </div>

                                {m.description && (
                                  <p className="text-[11px] text-slate-400 line-clamp-2 leading-relaxed">
                                    {m.description}
                                  </p>
                                )}
                              </div>

                              <div className="pt-2 border-t border-white/5 font-mono text-[11px]">
                                <div className="flex items-center justify-between text-slate-300">
                                  <span className="text-slate-500">Pricing / 1M:</span>
                                  <span>
                                    <span className="text-emerald-400 font-semibold">${m.input_price_per_m.toFixed(2)}</span>
                                    <span className="text-slate-600"> in / </span>
                                    <span className="text-amber-400 font-semibold">${m.output_price_per_m.toFixed(2)}</span>
                                    <span className="text-slate-600"> out</span>
                                  </span>
                                </div>
                                {m.cache_read_price_per_m > 0 && (
                                  <div className="flex items-center justify-between text-emerald-400 text-[10px] mt-0.5">
                                    <span>Prompt Cache:</span>
                                    <span>${m.cache_read_price_per_m.toFixed(3)}/M</span>
                                  </div>
                                )}
                              </div>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}

          {/* VIEW MODE 2: MULTI-PROVIDER COMPARISON (GROUPED) */}
          {catalogViewMode === 'compare' && (
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
              {filteredGroupedCatalog.map((g) => (
                <div
                  key={g.modelId}
                  className="panel hover:border-accent/40 transition-all space-y-3 bg-slate-900/70 flex flex-col justify-between"
                >
                  <div className="space-y-2">
                    {/* Header */}
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-semibold text-white text-sm truncate" title={g.name}>
                            {g.name}
                          </span>
                          {g.isCustom && (
                            <span className="text-[10px] bg-cyan-500/15 text-cyan-400 border border-cyan-500/30 px-1.5 rounded">
                              Custom
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-1.5 mt-0.5">
                          <span className="font-mono text-[11px] text-accent truncate">
                            {g.modelId}
                          </span>
                          <button
                            onClick={() => handleCopyModel(g.modelId)}
                            className="text-slate-500 hover:text-slate-300 p-0.5"
                            title="Copy Model ID"
                          >
                            {copiedModelId === g.modelId ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                          </button>
                        </div>
                      </div>

                      <div className="flex flex-col items-end gap-1 shrink-0">
                        <span className="chip bg-indigo-500/15 text-indigo-300 border border-indigo-500/20 text-[10px] font-mono">
                          {g.providers.length} {g.providers.length === 1 ? 'Provider' : 'Providers'}
                        </span>
                        {g.contextWindow > 0 && (
                          <span className="text-[10px] font-mono text-slate-400">
                            {(g.contextWindow / 1000).toFixed(0)}k ctx
                          </span>
                        )}
                      </div>
                    </div>

                    {g.description && (
                      <p className="text-xs text-slate-400 leading-relaxed line-clamp-2 min-h-[32px]">
                        {g.description}
                      </p>
                    )}
                  </div>

                  {/* Multi-Provider Comparative Matrix */}
                  <div className="space-y-1.5 pt-2 border-t border-white/5">
                    <div className="text-[10px] font-mono text-slate-400 uppercase tracking-wider flex items-center justify-between">
                      <span>Provider Pricing Comparison</span>
                      {g.providers.length > 1 && (
                        <span className="text-emerald-400 font-semibold flex items-center gap-1">
                          <TrendingDown className="h-3 w-3" /> Best: ${g.lowestInputPrice.toFixed(2)}/1M
                        </span>
                      )}
                    </div>

                    <div className="space-y-1 max-h-48 overflow-y-auto pr-0.5">
                      {g.providers.map((p) => {
                        const isCheapest = p.inputPrice > 0 && p.inputPrice === g.lowestInputPrice;
                        return (
                          <div
                            key={p.fullId}
                            className={`p-2 rounded-lg border text-xs font-mono transition-colors flex items-center justify-between gap-2 ${
                              isCheapest && g.providers.length > 1
                                ? 'bg-emerald-950/20 border-emerald-500/30'
                                : 'bg-slate-950/60 border-white/5'
                            }`}
                          >
                            <div className="min-w-0">
                              <div className="flex items-center gap-1.5">
                                <span className="font-semibold text-slate-200 truncate max-w-[130px]" title={p.provider}>
                                  {p.provider}
                                </span>
                                {p.isCanonical && (
                                  <span className="text-[9px] bg-indigo-500/20 text-indigo-300 border border-indigo-500/30 px-1 py-0.2 rounded shrink-0">
                                    1st-Party
                                  </span>
                                )}
                                {isCheapest && g.providers.length > 1 && (
                                  <span className="text-[9px] bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 px-1 py-0.2 rounded font-bold shrink-0">
                                    Lowest
                                  </span>
                                )}
                              </div>
                              {p.cacheReadPrice > 0 && (
                                <div className="text-[10px] text-emerald-400/90 truncate">
                                  ⚡ Cache: ${p.cacheReadPrice.toFixed(2)}/M
                                </div>
                              )}
                            </div>

                            <div className="text-right shrink-0">
                              <div className="text-slate-200 font-semibold">
                                <span className="text-emerald-400">${p.inputPrice.toFixed(2)}</span>
                                <span className="text-slate-500"> / </span>
                                <span className="text-amber-400">${p.outputPrice.toFixed(2)}</span>
                              </div>
                              <div className="text-[9px] text-slate-500">in / out (1M)</div>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* VIEW MODE 2: ALL PROVIDER VARIANTS (FLAT LIST) */}
          {catalogViewMode === 'flat' && (
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
              {filteredCatalog.map((m) => (
                <div key={m.id} className="panel hover:border-accent/40 transition-all space-y-3 bg-slate-900/60 flex flex-col justify-between">
                  <div className="space-y-2">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-semibold text-white text-sm truncate" title={m.name}>{m.name}</span>
                          {m.is_custom && (
                            <span className="text-[10px] bg-cyan-500/15 text-cyan-400 border border-cyan-500/30 px-1.5 rounded">
                              Custom
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-1.5 mt-0.5">
                          <span className="font-mono text-[11px] text-accent truncate" title={m.id}>
                            {m.id}
                          </span>
                          <button
                            onClick={() => handleCopyModel(m.id)}
                            className="text-slate-500 hover:text-slate-300 p-0.5"
                            title="Copy Full Model ID"
                          >
                            {copiedModelId === m.id ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                          </button>
                        </div>
                      </div>
                      <span className="chip bg-white/5 text-slate-300 text-[10px] font-mono shrink-0">
                        {m.provider}
                      </span>
                    </div>

                    <p className="text-xs text-slate-400 leading-relaxed min-h-[32px] line-clamp-2">{m.description}</p>
                  </div>

                  <div>
                    <div className="grid grid-cols-2 gap-2 text-xs border-t border-white/5 pt-3 font-mono">
                      <div className="panel-raised p-2">
                        <div className="text-[10px] text-slate-500">Input Tokens</div>
                        <div className="text-emerald-400 font-bold mt-0.5">${m.input_price_per_m.toFixed(2)} / 1M</div>
                      </div>
                      <div className="panel-raised p-2">
                        <div className="text-[10px] text-slate-500">Output Tokens</div>
                        <div className="text-amber-400 font-bold mt-0.5">${m.output_price_per_m.toFixed(2)} / 1M</div>
                      </div>
                    </div>

                    <div className="flex items-center justify-between text-[11px] text-slate-500 font-mono pt-2">
                      <span>Context: {(m.context_window / 1000).toFixed(0)}k tokens</span>
                      {m.cache_read_price_per_m > 0 && (
                        <span className="text-emerald-400 font-medium">⚡ Cache: ${m.cache_read_price_per_m.toFixed(3)}/M</span>
                      )}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* ------------------------------------------------------------- */}
      {/* 3. COST SIMULATOR TAB */}
      {/* ------------------------------------------------------------- */}
      {activeSubTab === 'calculator' && (
        <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
          <div className="lg:col-span-6 panel space-y-4">
            <div className="flex items-center gap-2 border-b border-white/5 pb-3">
              <Calculator className="h-5 w-5 text-accent" />
              <h3 className="font-semibold text-sm">Interactive Token & Spend Simulator</h3>
            </div>

            <div className="space-y-4 text-xs">
              <div>
                <label className="block text-slate-400 mb-1">Select AI Model</label>
                <select
                  value={calcModel || catalogList[0]?.id || ''}
                  onChange={(e) => setCalcModel(e.target.value)}
                  className="w-full p-2 bg-slate-900 border border-white/10 rounded-lg text-slate-200 font-mono focus:outline-none focus:border-accent"
                >
                  {catalogList.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.provider} — {m.name} (${m.input_price_per_m.toFixed(2)} in / ${m.output_price_per_m.toFixed(2)} out)
                    </option>
                  ))}
                </select>
              </div>

              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-slate-400">Prompt / Input Tokens</span>
                  <span className="font-mono text-slate-200">{promptTokens.toLocaleString()} tokens</span>
                </div>
                <input
                  type="range"
                  min="1000"
                  max="1000000"
                  step="5000"
                  value={promptTokens}
                  onChange={(e) => setPromptTokens(Number(e.target.value))}
                  className="w-full accent-indigo-500"
                />
              </div>

              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="text-slate-400">Completion / Output Tokens</span>
                  <span className="font-mono text-slate-200">{completionTokens.toLocaleString()} tokens</span>
                </div>
                <input
                  type="range"
                  min="500"
                  max="200000"
                  step="1000"
                  value={completionTokens}
                  onChange={(e) => setCompletionTokens(Number(e.target.value))}
                  className="w-full accent-indigo-500"
                />
              </div>
            </div>
          </div>

          <div className="lg:col-span-6 panel flex flex-col justify-center items-center text-center space-y-4 bg-gradient-to-br from-slate-900 to-slate-950 border-accent/30">
            <Coins className="h-8 w-8 text-yellow-400" />
            <div>
              <div className="text-xs text-slate-400 uppercase tracking-widest font-mono">Estimated Total Cost</div>
              <div className="text-4xl font-extrabold text-white font-mono mt-2">${calculatedCost.toFixed(4)}</div>
            </div>
            <p className="text-xs text-slate-400 max-w-sm">
              Estimated for {promptTokens.toLocaleString()} input tokens and {completionTokens.toLocaleString()} output tokens on {calcModel || catalogList[0]?.name || 'the selected model'}.
            </p>
          </div>
        </div>
      )}

      {/* ------------------------------------------------------------- */}
      {/* MODAL: ADD CUSTOM MODEL */}
      {/* ------------------------------------------------------------- */}
      {showAddModal && (
        <div className="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
          <div className="panel max-w-md w-full bg-slate-950 border-white/20 shadow-2xl space-y-4">
            <div className="flex items-center justify-between border-b border-white/10 pb-3">
              <h3 className="font-semibold text-sm text-white">Register Custom Model Specification</h3>
              <button
                onClick={() => setShowAddModal(false)}
                className="text-slate-400 hover:text-white text-xs"
              >
                ✕
              </button>
            </div>

            <form onSubmit={handleSaveCustomModel} className="space-y-3 text-xs">
              <div>
                <label className="block text-slate-400 mb-1">Model Identifier (ID)</label>
                <input
                  type="text"
                  required
                  placeholder="e.g. qwen-2.5-coder-local"
                  value={customId}
                  onChange={(e) => setCustomId(e.target.value)}
                  className="w-full p-2 bg-slate-900 border border-white/10 rounded-lg text-slate-200 font-mono focus:border-accent"
                />
              </div>

              <div>
                <label className="block text-slate-400 mb-1">Display Name</label>
                <input
                  type="text"
                  placeholder="e.g. Local Qwen 2.5 32B"
                  value={customName}
                  onChange={(e) => setCustomName(e.target.value)}
                  className="w-full p-2 bg-slate-900 border border-white/10 rounded-lg text-slate-200 focus:border-accent"
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-slate-400 mb-1">Input $/1M Tokens</label>
                  <input
                    type="number"
                    step="0.01"
                    value={customInPrice}
                    onChange={(e) => setCustomInPrice(e.target.value)}
                    className="w-full p-2 bg-slate-900 border border-white/10 rounded-lg text-slate-200 font-mono focus:border-accent"
                  />
                </div>
                <div>
                  <label className="block text-slate-400 mb-1">Output $/1M Tokens</label>
                  <input
                    type="number"
                    step="0.01"
                    value={customOutPrice}
                    onChange={(e) => setCustomOutPrice(e.target.value)}
                    className="w-full p-2 bg-slate-900 border border-white/10 rounded-lg text-slate-200 font-mono focus:border-accent"
                  />
                </div>
              </div>

              {saveError && (
                <div className="p-2 rounded-lg bg-rose-500/10 border border-rose-500/20 text-rose-300 text-xs flex items-start gap-2">
                  <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
                  <span className="break-all">{saveError}</span>
                </div>
              )}

              <div className="flex items-center justify-end gap-2 pt-2 border-t border-white/10">
                <button
                  type="button"
                  onClick={() => setShowAddModal(false)}
                  className="px-3 py-1.5 text-xs bg-slate-900 text-slate-300 rounded-lg border border-white/10"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={isSavingModel}
                  className="flex items-center gap-1.5 px-4 py-1.5 text-xs bg-accent text-white rounded-lg font-medium shadow-md shadow-accent/20 disabled:opacity-50"
                >
                  {saveSuccess ? (
                    <>
                      <Check className="h-3.5 w-3.5" />
                      Saved!
                    </>
                  ) : (
                    'Save Model'
                  )}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

