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
} from 'lucide-react';
import type { ActiveRun, ModelRow, Overview, ModelInfo } from '../types';
import { useModelCatalog } from '../hooks/useMetrics';

interface AgentSessionsViewProps {
  activeRuns: ActiveRun[];
  models: ModelRow[];
  overview?: Overview;
  loading: boolean;
}

export function AgentSessionsView({ activeRuns, models, overview, loading }: AgentSessionsViewProps) {
  const [search, setSearch] = useState('');
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  const [activeSubTab, setActiveSubTab] = useState<'sessions' | 'catalog' | 'calculator'>('sessions');

  // Model Catalog Query
  const catalogQuery = useModelCatalog();
  const catalogList = catalogQuery.data ?? [];

  // Cost Calculator State
  const [calcModel, setCalcModel] = useState<string>('claude-3-7-sonnet');
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

  const filteredRuns = useMemo(() => {
    const q = search.toLowerCase().trim();
    return activeRuns.filter((r) => {
      if (q && !r.agent_name.toLowerCase().includes(q) && !r.model_name.toLowerCase().includes(q) && !r.run_id.toLowerCase().includes(q)) {
        return false;
      }
      return true;
    });
  }, [activeRuns, search]);

  const selectedRun = useMemo(() => {
    if (selectedRunId) {
      const found = activeRuns.find((r) => r.run_id === selectedRunId);
      if (found) return found;
    }
    return activeRuns[0] ?? null;
  }, [selectedRunId, activeRuns]);

  // Filtered Catalog
  const filteredCatalog = useMemo(() => {
    const q = search.toLowerCase().trim();
    if (!q) return catalogList;
    return catalogList.filter(
      (m) =>
        m.name.toLowerCase().includes(q) ||
        m.id.toLowerCase().includes(q) ||
        m.provider.toLowerCase().includes(q)
    );
  }, [catalogList, search]);

  // Calculator Result
  const calculatedCost = useMemo(() => {
    const selected = catalogList.find((m) => m.id === calcModel);
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
      if (res.ok) {
        setSaveSuccess(true);
        catalogQuery.refetch();
        setTimeout(() => {
          setSaveSuccess(false);
          setShowAddModal(false);
          setCustomId('');
          setCustomName('');
        }, 1200);
      }
    } catch (err) {
      console.error('Failed to save custom model', err);
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
              <span className="text-xs font-normal text-slate-400">
                · {activeRuns.length} active sessions · {catalogList.length} catalog models
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
            Active Sessions ({activeRuns.length})
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
                          {r.model_name}
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
                      <div className="font-mono text-accent font-semibold mt-0.5">{selectedRun.model_name}</div>
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
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="relative">
              <Search className="h-3.5 w-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
              <input
                type="text"
                placeholder="Search models, providers…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="pl-8 pr-3 py-1.5 text-xs bg-slate-900 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent w-64"
              />
            </div>

            <button
              onClick={() => setShowAddModal(true)}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs bg-accent hover:bg-accent/90 text-white rounded-lg transition-all shadow-md shadow-accent/20 font-medium"
            >
              <Plus className="h-3.5 w-3.5" />
              Add Custom Model
            </button>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {filteredCatalog.map((m) => (
              <div key={m.id} className="panel hover:border-accent/40 transition-all space-y-3 bg-slate-900/60">
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="font-semibold text-white text-sm">{m.name}</span>
                      {m.is_custom && (
                        <span className="text-[10px] bg-cyan-500/15 text-cyan-400 border border-cyan-500/30 px-1.5 rounded">
                          Custom
                        </span>
                      )}
                    </div>
                    <div className="font-mono text-[11px] text-accent mt-0.5">{m.id}</div>
                  </div>
                  <span className="chip bg-white/5 text-slate-300 text-[10px] font-mono">
                    {m.provider}
                  </span>
                </div>

                <p className="text-xs text-slate-400 leading-relaxed min-h-[36px]">{m.description}</p>

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

                <div className="flex items-center justify-between text-[11px] text-slate-500 font-mono">
                  <span>Context: {(m.context_window / 1000).toFixed(0)}k tokens</span>
                  {m.cache_read_price_per_m > 0 && (
                    <span>Cache Read: ${m.cache_read_price_per_m.toFixed(3)}/M</span>
                  )}
                </div>
              </div>
            ))}
          </div>
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
                  value={calcModel}
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
              Estimated for {promptTokens.toLocaleString()} input tokens and {completionTokens.toLocaleString()} output tokens on {calcModel}.
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

