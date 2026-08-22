import { useState, useMemo } from 'react';
import {
  Network,
  Plus,
  Copy,
  Check,
  Trash2,
  Globe,
  Cpu,
  Zap,
  ArrowRight,
  ShieldCheck,
  Activity,
  Terminal,
  Clock,
  DollarSign,
  RefreshCw,
  Search,
  FileJson,
  MessageSquare,
  ChevronRight,
  Filter,
  Wrench,
  Brain,
  FileCode,
  FileText,
  Download,
  BarChart3,
  TrendingUp,
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
import type { ProxyRoute, ProxyTrafficRecord } from '../types';
import { useProxyRoutes, useModelCatalog, useProxyTraffic } from '../hooks/useMetrics';

export function ProxyRoutingView() {
  const { data: routes = [], refetch: refetchRoutes } = useProxyRoutes();
  const { data: catalog = [] } = useModelCatalog();
  const { data: traffic = [], refetch: refetchTraffic } = useProxyTraffic();

  const [activeSubTab, setActiveSubTab] = useState<'routes' | 'traffic'>('traffic');
  const [selectedTraffic, setSelectedTraffic] = useState<ProxyTrafficRecord | null>(null);
  const [trafficFilter, setTrafficFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState<'all' | '2xx' | 'errors' | 'tools' | 'cached' | 'reasoning'>('all');
  const [trafficInspectorTab, setTrafficInspectorTab] = useState<'analysis' | 'response' | 'request' | 'chat'>('analysis');
  const [trafficChartMode, setTrafficChartMode] = useState<'tokens' | 'cost'>('tokens');

  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [isModalOpen, setIsModalOpen] = useState(false);

  const [formName, setFormName] = useState('');
  const [formPath, setFormPath] = useState('');
  const [formUpstream, setFormUpstream] = useState('');
  const [formProtocol, setFormProtocol] = useState<'openai' | 'openai-compatible' | 'anthropic' | 'gemini' | 'custom'>('openai-compatible');
  const [formModel, setFormModel] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);

  const discoveredProviders = useMemo(() => {
    const provs = new Map<string, { name: string; api?: string; npm?: string; models: string[] }>();
    catalog.forEach((m) => {
      const p = m.provider || 'Custom';
      if (!provs.has(p)) {
        provs.set(p, { name: p, api: m.provider_api, npm: m.npm_package, models: [] });
      }
      const modelIdentifier = m.model_id || (m.id.includes('/') ? m.id.split('/')[1] : m.id);
      if (!provs.get(p)!.models.includes(modelIdentifier)) {
        provs.get(p)!.models.push(modelIdentifier);
      }
    });
    return Array.from(provs.values());
  }, [catalog]);

  const filteredTraffic = useMemo(() => {
    return traffic.filter((t) => {
      if (statusFilter === '2xx' && (t.status_code < 200 || t.status_code >= 300)) return false;
      if (statusFilter === 'errors' && t.status_code >= 200 && t.status_code < 300) return false;
      if (statusFilter === 'tools' && !(t.tool_calls && t.tool_calls.length > 0) && !(t.tool_count && t.tool_count > 0)) return false;
      if (statusFilter === 'cached' && !(t.cached_tokens && t.cached_tokens > 0)) return false;
      if (statusFilter === 'reasoning' && !t.reasoning && !(t.reasoning_tokens && t.reasoning_tokens > 0)) return false;
      if (!trafficFilter) return true;
      const q = trafficFilter.toLowerCase();
      return (
        t.model.toLowerCase().includes(q) ||
        t.provider.toLowerCase().includes(q) ||
        t.target_url.toLowerCase().includes(q) ||
        t.incoming_path.toLowerCase().includes(q) ||
        t.request_body.toLowerCase().includes(q) ||
        t.response_body.toLowerCase().includes(q)
      );
    });
  }, [traffic, trafficFilter, statusFilter]);

  const trafficAggregates = useMemo(() => {
    let totalPrompt = 0;
    let totalCompletion = 0;
    let totalCached = 0;
    let totalReasoning = 0;
    let totalCost = 0;
    let totalSavings = 0;
    let totalTools = 0;

    traffic.forEach((t) => {
      totalPrompt += t.prompt_tokens || 0;
      totalCompletion += t.completion_tokens || 0;
      totalCached += t.cached_tokens || 0;
      totalReasoning += t.reasoning_tokens || 0;
      totalCost += t.cost_usd || 0;
      totalSavings += t.cache_savings_usd || 0;
      totalTools += (t.tool_calls?.length || t.tool_count || 0);
    });

    const totalTokens = totalPrompt + totalCompletion;
    const avgCacheHit = totalPrompt > 0 ? (totalCached / totalPrompt) * 100 : 0;
    const avgReasoningRatio = totalCompletion > 0 ? (totalReasoning / totalCompletion) * 100 : 0;

    return {
      totalRequests: traffic.length,
      totalTokens,
      totalPrompt,
      totalCompletion,
      totalCached,
      totalReasoning,
      totalCost,
      totalSavings,
      totalTools,
      avgCacheHit,
      avgReasoningRatio,
    };
  }, [traffic]);

  // Token flow timeline data
  const tokenChartData = useMemo(() => {
    if (traffic.length === 0) return [];
    const sorted = [...traffic].sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
    return sorted.map((t, idx) => {
      const d = new Date(t.timestamp);
      return {
        id: idx + 1,
        time: `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`,
        prompt: t.prompt_tokens || 0,
        completion: t.completion_tokens || 0,
        cached: t.cached_tokens || 0,
        reasoning: t.reasoning_tokens || 0,
        cost: Number((t.cost_usd || 0).toFixed(4)),
        savings: Number((t.cache_savings_usd || 0).toFixed(4)),
        model: t.model,
      };
    });
  }, [traffic]);

  // Model breakdown data
  const modelBreakdownData = useMemo(() => {
    const map = new Map<string, { model: string; cost: number; savings: number; tokens: number; requests: number }>();
    traffic.forEach((t) => {
      const m = t.model || 'unknown';
      if (!map.has(m)) {
        map.set(m, { model: m, cost: 0, savings: 0, tokens: 0, requests: 0 });
      }
      const item = map.get(m)!;
      item.cost += t.cost_usd || 0;
      item.savings += t.cache_savings_usd || 0;
      item.tokens += (t.prompt_tokens || 0) + (t.completion_tokens || 0);
      item.requests += 1;
    });
    return Array.from(map.values()).map((v) => ({
      ...v,
      cost: Number(v.cost.toFixed(4)),
      savings: Number(v.savings.toFixed(4)),
    }));
  }, [traffic]);

  const activeSelectedTraffic = selectedTraffic || (filteredTraffic.length > 0 ? filteredTraffic[0] : null);

  const handleCopy = (id: string, text: string) => {
    navigator.clipboard.writeText(text);
    setCopiedId(id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  const handleClearTraffic = async () => {
    try {
      await fetch('/api/proxy/traffic', { method: 'DELETE' });
      setSelectedTraffic(null);
      refetchTraffic();
    } catch (err) {
      console.error(err);
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await fetch(`/api/proxy/routes/${id}`, { method: 'DELETE' });
      refetchRoutes();
    } catch (e) {
      console.error(e);
    }
  };

  const handleSaveRoute = async (e: React.FormEvent) => {
    e.preventDefault();
    setIsSubmitting(true);
    try {
      await fetch('/api/proxy/routes', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: formName,
          path_prefix: formPath.startsWith('/') ? formPath : '/' + formPath,
          target_upstream: formUpstream,
          protocol_type: formProtocol,
          default_model: formModel,
          enabled: true,
        }),
      });
      setIsModalOpen(false);
      setFormName('');
      setFormPath('');
      setFormUpstream('');
      setFormModel('');
      refetchRoutes();
    } catch (err) {
      console.error(err);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleSelectCatalogProvider = (providerName: string) => {
    const p = discoveredProviders.find((x) => x.name === providerName);
    if (!p) return;
    setFormName(`${p.name} Gateway`);
    setFormPath(`/proxy/${p.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`);
    if (p.api) {
      setFormUpstream(p.api);
    } else {
      setFormUpstream(`https://api.${p.name.toLowerCase().replace(/[^a-z0-9]+/g, '')}.com/v1`);
    }

    const lowerP = p.name.toLowerCase();
    const lowerNpm = (p.npm || '').toLowerCase();
    if (lowerP.includes('anthropic') || lowerNpm.includes('anthropic')) {
      setFormProtocol('anthropic');
    } else if (lowerP.includes('gemini') || lowerP.includes('google') || lowerNpm.includes('google')) {
      setFormProtocol('gemini');
    } else if (lowerP.includes('openai') && !lowerP.includes('compatible')) {
      setFormProtocol('openai');
    } else {
      setFormProtocol('openai-compatible');
    }

    if (p.models.length > 0) {
      setFormModel(p.models[0]);
    }
  };

  // Helper to safely format JSON
  const formatJSON = (raw: string) => {
    if (!raw) return '';
    try {
      const parsed = JSON.parse(raw);
      return JSON.stringify(parsed, null, 2);
    } catch {
      return raw;
    }
  };

  // Helper to generate replayable curl command
  const generateCurlCommand = (t: ProxyTrafficRecord) => {
    const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173';
    const url = `${origin}${t.incoming_path || '/proxy/custom'}`;
    const headerLines = Object.entries(t.request_headers || {})
      .filter(([k]) => !['host', 'content-length', 'connection'].includes(k.toLowerCase()))
      .map(([k, v]) => `-H "${k}: ${v}"`)
      .join(' \\\n  ');
    const escapedBody = (t.request_body || '').replace(/'/g, `'\\''`);
    return `curl -X ${t.method || 'POST'} "${url}" \\\n  ${headerLines ? headerLines + ' \\\n  ' : ''}-d '${escapedBody}'`;
  };

  // Export full wire traffic log as JSON
  const handleExportTraffic = () => {
    const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(traffic, null, 2));
    const downloadAnchor = document.createElement('a');
    downloadAnchor.setAttribute("href", dataStr);
    downloadAnchor.setAttribute("download", `wrongtrace-wire-traffic-${new Date().toISOString().slice(0, 10)}.json`);
    document.body.appendChild(downloadAnchor);
    downloadAnchor.click();
    downloadAnchor.remove();
  };

  // Extract messages array from request body
  const parsedMessages = useMemo(() => {
    if (!activeSelectedTraffic?.request_body) return [];
    try {
      const obj = JSON.parse(activeSelectedTraffic.request_body);
      return Array.isArray(obj.messages) ? obj.messages : [];
    } catch {
      return [];
    }
  }, [activeSelectedTraffic]);

  return (
    <div className="space-y-6">
      {/* Header & Sub-Tab Switcher */}
      <div className="panel flex flex-col md:flex-row items-start md:items-center justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <Network className="h-5 w-5 text-indigo-400" />
            <h2 className="text-base font-semibold tracking-tight">AI Gateway & Wire Traffic Inspector</h2>
          </div>
          <p className="text-xs text-slate-400 mt-1">
            Universal transparent proxy for any LLM provider. Inspect raw requests, responses, token telemetry, and wire payloads in real-time.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5">
            <button
              onClick={() => setActiveSubTab('traffic')}
              className={`flex items-center gap-1.5 px-3 py-1 text-xs rounded font-medium transition-all ${
                activeSubTab === 'traffic'
                  ? 'bg-indigo-600 text-white shadow'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              <Activity className="h-3.5 w-3.5" />
              <span>Live Traffic</span>
              <span className="text-[10px] px-1.5 py-0.2 rounded-full bg-white/20 font-mono">
                {traffic.length}
              </span>
            </button>
            <button
              onClick={() => setActiveSubTab('routes')}
              className={`flex items-center gap-1.5 px-3 py-1 text-xs rounded font-medium transition-all ${
                activeSubTab === 'routes'
                  ? 'bg-indigo-600 text-white shadow'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              <Globe className="h-3.5 w-3.5" />
              <span>Routes & Endpoints</span>
            </button>
          </div>

          {activeSubTab === 'routes' && (
            <button
              onClick={() => setIsModalOpen(true)}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium transition-colors shadow-lg shadow-indigo-600/20"
            >
              <Plus className="h-3.5 w-3.5" />
              Add Route
            </button>
          )}
        </div>
      </div>

      {activeSubTab === 'routes' && (
        <>
          {/* Zero-Config Direct Passthrough Banner */}
          <div className="panel bg-gradient-to-r from-indigo-950/40 via-purple-950/30 to-slate-900/40 border-indigo-500/20 p-4 rounded-xl space-y-2">
            <div className="flex items-center gap-2">
              <Zap className="h-4 w-4 text-amber-400" />
              <h3 className="text-xs font-semibold text-slate-200">Zero-Config Direct URL Passthrough</h3>
              <span className="text-[9px] bg-emerald-500/10 text-emerald-400 px-2 py-0.2 rounded border border-emerald-500/20 font-mono">No setup required</span>
            </div>
            <p className="text-xs text-slate-400">
              You can use WrongTrace as a transparent proxy without configuring routes! Simply prepend <code className="text-indigo-300 font-mono bg-white/5 px-1 py-0.5 rounded">{typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173'}/proxy/</code> to ANY upstream host or URL:
            </p>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-xs font-mono pt-1">
              <div className="p-2 bg-slate-950/70 rounded border border-white/5 flex items-center justify-between gap-2">
                <span className="truncate text-slate-300">
                  <span className="text-indigo-400">{typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173'}/proxy/</span>api.z.ai/api/coding/paas/v4
                </span>
                <button
                  onClick={() => handleCopy('direct-zai', `${typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173'}/proxy/api.z.ai/api/coding/paas/v4`)}
                  className="text-indigo-400 hover:text-indigo-300 shrink-0 flex items-center gap-1"
                >
                  {copiedId === 'direct-zai' ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                  {copiedId === 'direct-zai' ? 'Copied' : 'Copy'}
                </button>
              </div>
              <div className="p-2 bg-slate-950/70 rounded border border-white/5 flex items-center justify-between gap-2">
                <span className="truncate text-slate-300">
                  <span className="text-indigo-400">{typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173'}/proxy/</span>api.groq.com/openai/v1
                </span>
                <button
                  onClick={() => handleCopy('direct-groq', `${typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173'}/proxy/api.groq.com/openai/v1`)}
                  className="text-indigo-400 hover:text-indigo-300 shrink-0 flex items-center gap-1"
                >
                  {copiedId === 'direct-groq' ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                  {copiedId === 'direct-groq' ? 'Copied' : 'Copy'}
                </button>
              </div>
            </div>
          </div>

          {/* Routes Grid */}
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {routes.map((route: ProxyRoute) => {
              const fullEndpoint = `${window.location.origin}${route.path_prefix}`;
              return (
                <div key={route.id} className="panel space-y-3 relative group">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className="h-2 w-2 rounded-full bg-emerald-400 ring-4 ring-emerald-500/20" />
                      <span className="font-semibold text-slate-200 text-sm">{route.name}</span>
                    </div>
                    <span className="text-[10px] px-2 py-0.5 rounded bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 font-mono uppercase">
                      {route.protocol_type}
                    </span>
                  </div>

                  {/* Endpoint URLs */}
                  <div className="space-y-1.5 text-xs font-mono">
                    <div className="p-2 bg-slate-900/80 rounded border border-white/5 space-y-1">
                      <div className="text-[10px] text-slate-500 flex items-center justify-between">
                        <span>AGENT BASE_URL</span>
                        <button
                          onClick={() => handleCopy(route.id, fullEndpoint)}
                          className="text-indigo-400 hover:text-indigo-300 flex items-center gap-1"
                        >
                          {copiedId === route.id ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                          {copiedId === route.id ? 'Copied' : 'Copy'}
                        </button>
                      </div>
                      <div className="text-slate-300 truncate" title={fullEndpoint}>
                        {fullEndpoint}
                      </div>
                    </div>

                    <div className="flex items-center gap-1.5 text-[11px] text-slate-400">
                      <ArrowRight className="h-3 w-3 text-slate-500 flex-shrink-0" />
                      <span className="truncate text-slate-500" title={route.target_upstream}>
                        {route.target_upstream}
                      </span>
                    </div>
                  </div>

                  {/* Footer */}
                  <div className="flex items-center justify-between pt-2 border-t border-white/5 text-xs">
                    <div className="flex items-center gap-1.5 text-[11px] text-slate-400 font-mono">
                      <Cpu className="h-3 w-3 text-slate-500" />
                      <span>{route.default_model || 'Any Model'}</span>
                    </div>
                    <button
                      onClick={() => handleDelete(route.id)}
                      className="opacity-0 group-hover:opacity-100 p-1 text-rose-400 hover:bg-rose-500/10 rounded transition-all"
                      title="Delete Route"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        </>
      )}

      {activeSubTab === 'traffic' && (
        <div className="space-y-4">
          {/* Aggregate Telemetry Header */}
          {traffic.length > 0 && (
            <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3">
              <div className="p-3 bg-slate-900/80 rounded-xl border border-white/5 space-y-1 font-mono">
                <div className="text-[10px] text-slate-400">PROXY REQUESTS</div>
                <div className="text-base font-bold text-slate-200">{trafficAggregates.totalRequests}</div>
                <div className="text-[10px] text-slate-500">Live intercepted</div>
              </div>

              <div className="p-3 bg-slate-900/80 rounded-xl border border-white/5 space-y-1 font-mono">
                <div className="text-[10px] text-slate-400">TOTAL TOKENS</div>
                <div className="text-base font-bold text-slate-200">{trafficAggregates.totalTokens.toLocaleString()}</div>
                <div className="text-[10px] text-slate-500">{trafficAggregates.totalPrompt.toLocaleString()} in / {trafficAggregates.totalCompletion.toLocaleString()} out</div>
              </div>

              <div className="p-3 bg-slate-900/80 rounded-xl border border-emerald-500/20 bg-emerald-950/10 space-y-1 font-mono">
                <div className="text-[10px] text-emerald-400 flex items-center justify-between">
                  <span className="flex items-center gap-1"><Zap className="h-3 w-3" /> PROMPT CACHE</span>
                  <span className="text-[9px] bg-emerald-500/20 text-emerald-300 px-1 rounded">EXACT CACHE</span>
                </div>
                <div className="text-base font-bold text-emerald-300">
                  {trafficAggregates.totalCached > 0 ? `${trafficAggregates.avgCacheHit.toFixed(1)}%` : '0.0%'}
                </div>
                <div className="text-[10px] text-emerald-400/80 truncate">
                  ${trafficAggregates.totalSavings.toFixed(4)} saved at $0
                </div>
              </div>

              <div className="p-3 bg-slate-900/80 rounded-xl border border-cyan-500/20 bg-cyan-950/10 space-y-1 font-mono">
                <div className="text-[10px] text-cyan-400 flex items-center gap-1">
                  <ShieldCheck className="h-3 w-3" /> SECRET SCANNER
                </div>
                <div className="text-base font-bold text-cyan-300">
                  Active
                </div>
                <div className="text-[10px] text-cyan-400/80 truncate">
                  Auto-redacts leaked API keys & DB passwords
                </div>
              </div>

              <div className="p-3 bg-slate-900/80 rounded-xl border border-purple-500/20 bg-purple-950/10 space-y-1 font-mono">
                <div className="text-[10px] text-purple-400 flex items-center gap-1">
                  <Brain className="h-3 w-3" /> REASONING / COT
                </div>
                <div className="text-base font-bold text-purple-300">
                  {trafficAggregates.totalReasoning > 0 ? `${trafficAggregates.avgReasoningRatio.toFixed(1)}%` : '0.0%'}
                </div>
                <div className="text-[10px] text-purple-400/80 truncate">
                  {trafficAggregates.totalReasoning.toLocaleString()} think tokens
                </div>
              </div>

              <div className="p-3 bg-slate-900/80 rounded-xl border border-amber-500/20 bg-amber-950/10 space-y-1 font-mono">
                <div className="text-[10px] text-amber-400 flex items-center gap-1">
                  <Wrench className="h-3 w-3" /> TOOL CALLS
                </div>
                <div className="text-base font-bold text-amber-300">{trafficAggregates.totalTools}</div>
                <div className="text-[10px] text-slate-400 truncate">
                  ${trafficAggregates.totalCost.toFixed(4)} total spend
                </div>
              </div>
            </div>
          )}

          {/* Token Flow Velocity & Spend Breakdown Chart */}
          {traffic.length > 0 && (
            <div className="panel space-y-4">
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/5 pb-3">
                <div className="flex items-center gap-2">
                  <BarChart3 className="h-4 w-4 text-indigo-400" />
                  <h3 className="font-semibold tracking-tight text-sm text-white">
                    Live Wire Token Velocity & LLM Spend Analytics
                  </h3>
                </div>
                <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5 text-xs">
                  <button
                    onClick={() => setTrafficChartMode('tokens')}
                    className={`px-2.5 py-1 rounded font-medium transition-all ${
                      trafficChartMode === 'tokens'
                        ? 'bg-indigo-600 text-white shadow-sm'
                        : 'text-slate-400 hover:text-slate-200'
                    }`}
                  >
                    Token Composition
                  </button>
                  <button
                    onClick={() => setTrafficChartMode('cost')}
                    className={`px-2.5 py-1 rounded font-medium transition-all ${
                      trafficChartMode === 'cost'
                        ? 'bg-indigo-600 text-white shadow-sm'
                        : 'text-slate-400 hover:text-slate-200'
                    }`}
                  >
                    Cost & Cache Savings
                  </button>
                </div>
              </div>

              <div className="h-56">
                <ResponsiveContainer width="100%" height="100%">
                  {trafficChartMode === 'tokens' ? (
                    <AreaChart data={tokenChartData} margin={{ top: 8, right: 10, left: -10, bottom: 0 }}>
                      <defs>
                        <linearGradient id="promptGrad" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="5%" stopColor="#818cf8" stopOpacity={0.4} />
                          <stop offset="95%" stopColor="#818cf8" stopOpacity={0.0} />
                        </linearGradient>
                        <linearGradient id="compGrad" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="5%" stopColor="#38bdf8" stopOpacity={0.4} />
                          <stop offset="95%" stopColor="#38bdf8" stopOpacity={0.0} />
                        </linearGradient>
                        <linearGradient id="cacheGrad" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="5%" stopColor="#34d399" stopOpacity={0.4} />
                          <stop offset="95%" stopColor="#34d399" stopOpacity={0.0} />
                        </linearGradient>
                        <linearGradient id="reasonGrad" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="5%" stopColor="#c084fc" stopOpacity={0.4} />
                          <stop offset="95%" stopColor="#c084fc" stopOpacity={0.0} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                      <XAxis dataKey="time" stroke="#64748b" tick={{ fontSize: 11 }} />
                      <YAxis stroke="#64748b" tick={{ fontSize: 11 }} />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: '#0f172a',
                          borderColor: 'rgba(255,255,255,0.1)',
                          borderRadius: 8,
                          fontSize: 12,
                          color: '#f8fafc',
                        }}
                      />
                      <Legend wrapperStyle={{ fontSize: 11 }} />
                      <Area
                        type="monotone"
                        dataKey="prompt"
                        name="Prompt Tokens"
                        stroke="#818cf8"
                        strokeWidth={1.5}
                        fill="url(#promptGrad)"
                      />
                      <Area
                        type="monotone"
                        dataKey="completion"
                        name="Completion Tokens"
                        stroke="#38bdf8"
                        strokeWidth={1.5}
                        fill="url(#compGrad)"
                      />
                      <Area
                        type="monotone"
                        dataKey="cached"
                        name="Cached Tokens"
                        stroke="#34d399"
                        strokeWidth={1.5}
                        fill="url(#cacheGrad)"
                      />
                      <Area
                        type="monotone"
                        dataKey="reasoning"
                        name="Reasoning / CoT"
                        stroke="#c084fc"
                        strokeWidth={1.5}
                        fill="url(#reasonGrad)"
                      />
                    </AreaChart>
                  ) : (
                    <BarChart data={modelBreakdownData} margin={{ top: 8, right: 10, left: -10, bottom: 0 }}>
                      <CartesianGrid stroke="rgba(255,255,255,0.05)" vertical={false} />
                      <XAxis dataKey="model" stroke="#64748b" tick={{ fontSize: 11 }} />
                      <YAxis stroke="#64748b" tick={{ fontSize: 11 }} unit="$" />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: '#0f172a',
                          borderColor: 'rgba(255,255,255,0.1)',
                          borderRadius: 8,
                          fontSize: 12,
                        }}
                        formatter={(val: any, name: any) => [`$${val}`, name]}
                      />
                      <Legend wrapperStyle={{ fontSize: 11 }} />
                      <Bar dataKey="cost" name="Model Spend ($)" fill="#f43f5e" radius={[4, 4, 0, 0]} />
                      <Bar dataKey="savings" name="Cache Savings ($)" fill="#10b981" radius={[4, 4, 0, 0]} />
                    </BarChart>
                  )}
                </ResponsiveContainer>
              </div>
            </div>
          )}

          {/* Traffic Toolbar */}
          <div className="flex flex-wrap items-center justify-between gap-3 bg-slate-900/60 p-3 rounded-xl border border-white/5">
            <div className="flex items-center gap-2 flex-1 max-w-md">
              <Search className="h-4 w-4 text-slate-400 shrink-0" />
              <input
                type="text"
                value={trafficFilter}
                onChange={(e) => setTrafficFilter(e.target.value)}
                placeholder="Search raw payload, model, provider, path..."
                className="w-full bg-slate-950/80 border border-white/10 rounded-lg px-2.5 py-1 text-xs text-slate-200 placeholder-slate-500 focus:outline-none focus:border-indigo-500"
              />
            </div>

            <div className="flex items-center gap-2 text-xs flex-wrap">
              <div className="flex items-center bg-slate-950 rounded-lg p-0.5 border border-white/10 font-mono text-[11px] flex-wrap gap-0.5">
                <button
                  onClick={() => setStatusFilter('all')}
                  className={`px-2 py-1 rounded transition-colors ${statusFilter === 'all' ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}
                >
                  All ({traffic.length})
                </button>
                <button
                  onClick={() => setStatusFilter('2xx')}
                  className={`px-2 py-1 rounded transition-colors ${statusFilter === '2xx' ? 'bg-emerald-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}
                >
                  2xx OK
                </button>
                <button
                  onClick={() => setStatusFilter('tools')}
                  className={`px-2 py-1 rounded transition-colors ${statusFilter === 'tools' ? 'bg-amber-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}
                >
                  🛠️ Tools ({traffic.filter((t) => (t.tool_calls && t.tool_calls.length > 0) || (t.tool_count && t.tool_count > 0)).length})
                </button>
                <button
                  onClick={() => setStatusFilter('cached')}
                  className={`px-2 py-1 rounded transition-colors ${statusFilter === 'cached' ? 'bg-teal-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}
                >
                  ⚡ Cache ({traffic.filter((t) => t.cached_tokens && t.cached_tokens > 0).length})
                </button>
                <button
                  onClick={() => setStatusFilter('reasoning')}
                  className={`px-2 py-1 rounded transition-colors ${statusFilter === 'reasoning' ? 'bg-purple-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}
                >
                  🧠 Think ({traffic.filter((t) => t.reasoning || (t.reasoning_tokens && t.reasoning_tokens > 0)).length})
                </button>
                <button
                  onClick={() => setStatusFilter('errors')}
                  className={`px-2 py-1 rounded transition-colors ${statusFilter === 'errors' ? 'bg-rose-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}
                >
                  Errors
                </button>
              </div>

              <button
                onClick={handleExportTraffic}
                disabled={traffic.length === 0}
                className="flex items-center gap-1 px-2.5 py-1.5 rounded-lg bg-slate-800 hover:bg-indigo-950/60 hover:text-indigo-300 text-slate-300 border border-white/5 transition-colors disabled:opacity-50 font-mono text-[11px]"
                title="Export Traffic Log as JSON"
              >
                <Download className="h-3.5 w-3.5" />
                <span>Export</span>
              </button>

              <button
                onClick={() => refetchTraffic()}
                className="p-1.5 rounded-lg bg-slate-800 hover:bg-slate-700 text-slate-300 border border-white/5"
                title="Refresh Traffic Log"
              >
                <RefreshCw className="h-3.5 w-3.5" />
              </button>

              <button
                onClick={handleClearTraffic}
                disabled={traffic.length === 0}
                className="px-2.5 py-1.5 rounded-lg bg-slate-800 hover:bg-rose-950/40 hover:text-rose-300 text-slate-400 border border-white/5 transition-colors disabled:opacity-50"
              >
                Clear Log
              </button>
            </div>
          </div>

          {filteredTraffic.length === 0 ? (
            <div className="panel text-center py-12 space-y-3">
              <Activity className="h-10 w-10 text-slate-600 mx-auto animate-pulse" />
              <div className="text-sm font-semibold text-slate-300">No Gateway Traffic Intercepted Yet</div>
              <p className="text-xs text-slate-500 max-w-md mx-auto">
                Send an LLM request through <code className="text-indigo-300 font-mono bg-white/5 px-1 py-0.5 rounded">{typeof window !== 'undefined' ? window.location.origin : 'http://localhost:5173'}/proxy/api.z.ai/api/coding/paas/v4</code> to inspect raw wire traffic in real-time.
              </p>
            </div>
          ) : (
            <div className="grid grid-cols-1 lg:grid-cols-12 gap-4">
              {/* Traffic List (Left 5 cols) */}
              <div className="lg:col-span-5 space-y-2 max-h-[750px] overflow-y-auto pr-1">
                {filteredTraffic.map((t) => {
                  const isSelected = activeSelectedTraffic?.id === t.id;
                  const isSuccess = t.status_code >= 200 && t.status_code < 300;
                  return (
                    <div
                      key={t.id}
                      onClick={() => setSelectedTraffic(t)}
                      className={`p-3 rounded-xl border transition-all cursor-pointer space-y-2 ${
                        isSelected
                          ? 'bg-slate-850 border-indigo-500 shadow-md shadow-indigo-500/10'
                          : 'bg-slate-900/70 border-white/5 hover:border-white/20 hover:bg-slate-800/60'
                      }`}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <div className="flex items-center gap-2 min-w-0">
                          <span
                            className={`px-1.5 py-0.5 rounded text-[10px] font-mono font-bold ${
                              isSuccess
                                ? 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/30'
                                : 'bg-rose-500/20 text-rose-400 border border-rose-500/30'
                            }`}
                          >
                            {t.status_code}
                          </span>
                          <span className="text-xs font-mono font-semibold text-slate-200 truncate">
                            {t.model}
                          </span>
                        </div>
                        <span className="text-[10px] font-mono text-slate-500 shrink-0">
                          {t.duration_ms}ms
                        </span>
                      </div>

                      <div className="flex items-center justify-between text-[11px] text-slate-400 font-mono">
                        <span className="text-indigo-400 font-medium truncate">{t.provider}</span>
                        <span>{t.total_tokens > 0 ? `${t.total_tokens.toLocaleString()} tok` : (t.is_stream ? 'Streaming' : '-')}</span>
                        {t.cost_usd > 0 && <span className="text-emerald-400">${t.cost_usd.toFixed(4)}</span>}
                      </div>

                      {/* Tool Calls, Reasoning, & Cache Badges */}
                      <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                        {t.id.includes('cache_hit') || (t.cost_usd === 0 && (t.cached_tokens || 0) > 0) ? (
                          <span className="inline-flex items-center gap-1 text-[10px] font-mono bg-emerald-500/20 text-emerald-300 border border-emerald-500/40 px-1.5 py-0.5 rounded font-bold">
                            <Zap className="h-2.5 w-2.5 text-amber-300" />
                            <span>CACHE HIT ($0)</span>
                          </span>
                        ) : null}

                        {t.tool_calls && t.tool_calls.length > 0 ? (
                          <span className="inline-flex items-center gap-1 text-[10px] font-mono bg-amber-500/15 text-amber-300 border border-amber-500/30 px-1.5 py-0.5 rounded">
                            <Wrench className="h-2.5 w-2.5" />
                            <span className="truncate max-w-[150px]">
                              {t.tool_calls[0].name}
                              {t.tool_calls.length > 1 ? ` (+${t.tool_calls.length - 1})` : ''}
                            </span>
                          </span>
                        ) : null}

                        {t.reasoning ? (
                          <span className="inline-flex items-center gap-1 text-[10px] font-mono bg-purple-500/15 text-purple-300 border border-purple-500/30 px-1.5 py-0.5 rounded">
                            <Brain className="h-2.5 w-2.5" />
                            <span>Thinking</span>
                          </span>
                        ) : null}

                        {t.finish_reason && (
                          <span className="text-[9px] font-mono text-slate-500 bg-white/5 px-1 py-0.2 rounded">
                            {t.finish_reason}
                          </span>
                        )}
                      </div>

                      <div className="text-[10px] font-mono text-slate-500 truncate" title={t.target_url}>
                        {t.target_url}
                      </div>
                    </div>
                  );
                })}
              </div>

              {/* Raw Payload & Analysis Inspector (Right 7 cols) */}
              <div className="lg:col-span-7">
                {activeSelectedTraffic ? (
                  <div className="panel space-y-4 max-h-[750px] overflow-y-auto">
                    {/* Header Summary */}
                    <div className="flex flex-wrap items-center justify-between gap-2 pb-3 border-b border-white/10">
                      <div className="flex items-center gap-2">
                        <span
                          className={`px-2 py-0.5 rounded text-xs font-mono font-bold ${
                            activeSelectedTraffic.status_code >= 200 && activeSelectedTraffic.status_code < 300
                              ? 'bg-emerald-500/20 text-emerald-400 border border-emerald-500/30'
                              : 'bg-rose-500/20 text-rose-400 border border-rose-500/30'
                          }`}
                        >
                          {activeSelectedTraffic.status_code} {activeSelectedTraffic.method}
                        </span>
                        <span className="font-semibold text-slate-200 text-sm font-mono">
                          {activeSelectedTraffic.model}
                        </span>
                        <span className="text-[11px] text-indigo-400 font-mono bg-indigo-500/10 px-2 py-0.5 rounded">
                          {activeSelectedTraffic.provider}
                        </span>
                      </div>

                      <div className="flex items-center gap-3 text-xs font-mono text-slate-400">
                        <span className="flex items-center gap-1">
                          <Clock className="h-3.5 w-3.5 text-slate-500" />
                          {activeSelectedTraffic.duration_ms}ms
                        </span>
                        {activeSelectedTraffic.total_tokens > 0 && (
                          <span className="text-slate-300">
                            {activeSelectedTraffic.prompt_tokens} in / {activeSelectedTraffic.completion_tokens} out
                          </span>
                        )}
                        {activeSelectedTraffic.cost_usd > 0 && (
                          <span className="text-emerald-400 font-semibold">
                            ${activeSelectedTraffic.cost_usd.toFixed(4)}
                          </span>
                        )}
                      </div>
                    </div>

                    {/* Upstream URL */}
                    <div className="p-2 bg-slate-950/80 rounded-lg border border-white/5 font-mono text-xs flex items-center justify-between gap-2">
                      <span className="truncate text-slate-300" title={activeSelectedTraffic.target_url}>
                        <span className="text-slate-500">FORWARDED TO: </span>
                        {activeSelectedTraffic.target_url}
                      </span>
                      <button
                        onClick={() => handleCopy('url-' + activeSelectedTraffic.id, activeSelectedTraffic.target_url)}
                        className="text-slate-400 hover:text-slate-200 shrink-0"
                        title="Copy URL"
                      >
                        {copiedId === 'url-' + activeSelectedTraffic.id ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
                      </button>
                    </div>

                    {/* Payload Subtabs */}
                    <div className="flex items-center justify-between border-b border-white/10 pb-2">
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => setTrafficInspectorTab('analysis')}
                          className={`flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-all ${
                            trafficInspectorTab === 'analysis'
                              ? 'bg-indigo-600 text-white shadow'
                              : 'text-slate-400 hover:text-slate-200'
                          }`}
                        >
                          <Wrench className="h-3.5 w-3.5 text-amber-400" />
                          <span>Tool & Semantic Analysis</span>
                          {activeSelectedTraffic.tool_calls && activeSelectedTraffic.tool_calls.length > 0 && (
                            <span className="text-[10px] px-1 rounded-full bg-amber-400/20 text-amber-300 font-mono">
                              {activeSelectedTraffic.tool_calls.length}
                            </span>
                          )}
                        </button>
                        <button
                          onClick={() => setTrafficInspectorTab('response')}
                          className={`flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-all ${
                            trafficInspectorTab === 'response'
                              ? 'bg-indigo-600 text-white shadow'
                              : 'text-slate-400 hover:text-slate-200'
                          }`}
                        >
                          <FileJson className="h-3.5 w-3.5" />
                          <span>Raw Response</span>
                        </button>
                        <button
                          onClick={() => setTrafficInspectorTab('request')}
                          className={`flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-all ${
                            trafficInspectorTab === 'request'
                              ? 'bg-indigo-600 text-white shadow'
                              : 'text-slate-400 hover:text-slate-200'
                          }`}
                        >
                          <FileJson className="h-3.5 w-3.5" />
                          <span>Raw Request</span>
                        </button>
                        {parsedMessages.length > 0 && (
                          <button
                            onClick={() => setTrafficInspectorTab('chat')}
                            className={`flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-all ${
                              trafficInspectorTab === 'chat'
                                ? 'bg-indigo-600 text-white shadow'
                                : 'text-slate-400 hover:text-slate-200'
                            }`}
                          >
                            <MessageSquare className="h-3.5 w-3.5" />
                            <span>Messages ({parsedMessages.length})</span>
                          </button>
                        )}
                      </div>

                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => handleCopy('curl-' + activeSelectedTraffic.id, generateCurlCommand(activeSelectedTraffic))}
                          className="text-xs font-mono text-amber-400 hover:text-amber-300 flex items-center gap-1 bg-amber-500/10 hover:bg-amber-500/20 px-2.5 py-1 rounded border border-amber-500/20 transition-colors"
                          title="Copy replayable cURL terminal command"
                        >
                          <Terminal className="h-3.5 w-3.5" />
                          <span>{copiedId === 'curl-' + activeSelectedTraffic.id ? 'cURL Copied!' : 'Copy cURL'}</span>
                        </button>

                        <button
                          onClick={() =>
                            handleCopy(
                              'payload-' + activeSelectedTraffic.id,
                              trafficInspectorTab === 'request'
                                ? activeSelectedTraffic.request_body
                                : activeSelectedTraffic.response_body
                            )
                          }
                          className="text-xs font-mono text-indigo-400 hover:text-indigo-300 flex items-center gap-1 bg-indigo-500/10 hover:bg-indigo-500/20 px-2.5 py-1 rounded border border-indigo-500/20 transition-colors"
                        >
                          {copiedId === 'payload-' + activeSelectedTraffic.id ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
                          <span>Copy Payload</span>
                        </button>
                      </div>
                    </div>

                    {/* Content View */}
                    {trafficInspectorTab === 'analysis' && (
                      <div className="space-y-4">
                        {/* Tool Calls Section */}
                        {activeSelectedTraffic.tool_calls && activeSelectedTraffic.tool_calls.length > 0 ? (
                          <div className="space-y-2">
                            <div className="flex items-center justify-between text-xs font-semibold text-amber-300">
                              <span className="flex items-center gap-1.5">
                                <Wrench className="h-4 w-4 text-amber-400" />
                                Detected Tool Invocations ({activeSelectedTraffic.tool_calls.length})
                              </span>
                              <span className="text-[10px] font-mono text-slate-500 uppercase">
                                Action Payload Extracted
                              </span>
                            </div>

                            <div className="space-y-2">
                              {activeSelectedTraffic.tool_calls.map((tool, idx) => (
                                <div
                                  key={idx}
                                  className="p-3 bg-slate-950/80 rounded-xl border border-amber-500/20 space-y-2"
                                >
                                  <div className="flex items-center justify-between gap-2">
                                    <div className="flex items-center gap-2">
                                      <span className="px-2 py-0.5 rounded bg-amber-500/20 text-amber-300 border border-amber-500/30 text-xs font-mono font-bold">
                                        {tool.name}
                                      </span>
                                      {tool.id && (
                                        <span className="text-[10px] font-mono text-slate-500">
                                          ID: {tool.id}
                                        </span>
                                      )}
                                    </div>

                                    {tool.target_file && (
                                      <div className="flex items-center gap-1 text-[11px] font-mono text-indigo-300 bg-indigo-500/10 px-2 py-0.5 rounded border border-indigo-500/20">
                                        <FileCode className="h-3 w-3 text-indigo-400" />
                                        <span className="truncate max-w-[240px]">{tool.target_file}</span>
                                      </div>
                                    )}
                                  </div>

                                  {tool.arguments && (
                                    <pre className="p-2 bg-slate-900/90 rounded border border-white/5 font-mono text-[11px] text-slate-300 overflow-x-auto whitespace-pre-wrap max-h-48 leading-relaxed">
                                      {formatJSON(tool.arguments)}
                                    </pre>
                                  )}
                                </div>
                              ))}
                            </div>
                          </div>
                        ) : null}

                        {/* Reasoning / Chain of Thought */}
                        {activeSelectedTraffic.reasoning ? (
                          <div className="space-y-2">
                            <div className="flex items-center gap-1.5 text-xs font-semibold text-purple-300">
                              <Brain className="h-4 w-4 text-purple-400" />
                              Reasoning & Thinking Process (DeepSeek R1 / CoT)
                            </div>
                            <div className="p-3 bg-purple-950/20 border border-purple-500/20 rounded-xl text-xs font-mono text-purple-200/90 whitespace-pre-wrap leading-relaxed max-h-60 overflow-y-auto">
                              {activeSelectedTraffic.reasoning}
                            </div>
                          </div>
                        ) : null}

                        {/* Assistant Reply */}
                        {activeSelectedTraffic.assistant_reply ? (
                          <div className="space-y-2">
                            <div className="flex items-center gap-1.5 text-xs font-semibold text-emerald-300">
                              <MessageSquare className="h-4 w-4 text-emerald-400" />
                              Model Response Text
                            </div>
                            <div className="p-3 bg-slate-950/80 border border-white/5 rounded-xl text-xs text-slate-200 whitespace-pre-wrap leading-relaxed max-h-60 overflow-y-auto">
                              {activeSelectedTraffic.assistant_reply}
                            </div>
                          </div>
                        ) : null}

                        {/* Telemetry & Context Card */}
                        <div className="p-3 bg-slate-950/60 rounded-xl border border-white/5 grid grid-cols-2 md:grid-cols-4 gap-3 text-xs font-mono">
                          <div>
                            <div className="text-[10px] text-slate-500">MESSAGES IN SESSION</div>
                            <div className="text-slate-200 font-semibold">{activeSelectedTraffic.message_count || (parsedMessages.length > 0 ? parsedMessages.length : 1)}</div>
                          </div>
                          <div>
                            <div className="text-[10px] text-slate-500">FINISH REASON</div>
                            <div className="text-indigo-400 font-semibold">{activeSelectedTraffic.finish_reason || 'stop'}</div>
                          </div>
                          <div>
                            <div className="text-[10px] text-slate-500">AGENT TRACE</div>
                            <div className="text-slate-300 truncate">{activeSelectedTraffic.agent_name || 'AI Gateway'}</div>
                          </div>
                          <div>
                            <div className="text-[10px] text-slate-500">TASK ID</div>
                            <div className="text-slate-300 truncate">{activeSelectedTraffic.task_id || 'ProxyGateway'}</div>
                          </div>
                        </div>

                        {/* Detailed Prompt Caching & Reasoning Breakdown Card */}
                        {(activeSelectedTraffic.total_tokens > 0 || (activeSelectedTraffic.cached_tokens && activeSelectedTraffic.cached_tokens > 0)) && (
                          <div className="p-3.5 bg-gradient-to-r from-slate-950/90 via-slate-900/60 to-slate-950/90 rounded-xl border border-white/10 space-y-3">
                            <div className="flex items-center justify-between text-xs">
                              <span className="font-semibold text-slate-200 flex items-center gap-1.5 font-mono">
                                <Zap className="h-3.5 w-3.5 text-amber-400" />
                                Wire Token Telemetry & Cache Breakdown
                              </span>
                              {activeSelectedTraffic.cache_hit_rate ? (
                                <span className="px-2 py-0.5 rounded bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 text-[10px] font-mono font-bold">
                                  {activeSelectedTraffic.cache_hit_rate.toFixed(1)}% Cache Hit
                                </span>
                              ) : null}
                            </div>

                            <div className="grid grid-cols-2 md:grid-cols-4 gap-2.5 text-xs font-mono">
                              {/* Prompt Tokens */}
                              <div className="p-2 bg-slate-900/70 rounded-lg border border-white/5 space-y-1">
                                <div className="text-[10px] text-slate-400">PROMPT TOKENS</div>
                                <div className="text-slate-200 font-bold text-sm">
                                  {activeSelectedTraffic.prompt_tokens.toLocaleString()}
                                </div>
                                {activeSelectedTraffic.cached_tokens && activeSelectedTraffic.cached_tokens > 0 ? (
                                  <div className="text-[10px] text-emerald-400 font-medium">
                                    ⚡ {activeSelectedTraffic.cached_tokens.toLocaleString()} cached
                                  </div>
                                ) : (
                                  <div className="text-[10px] text-slate-500">No cache hits</div>
                                )}
                              </div>

                              {/* Completion Tokens */}
                              <div className="p-2 bg-slate-900/70 rounded-lg border border-white/5 space-y-1">
                                <div className="text-[10px] text-slate-400">OUTPUT TOKENS</div>
                                <div className="text-slate-200 font-bold text-sm">
                                  {activeSelectedTraffic.completion_tokens.toLocaleString()}
                                </div>
                                {activeSelectedTraffic.reasoning_tokens && activeSelectedTraffic.reasoning_tokens > 0 ? (
                                  <div className="text-[10px] text-purple-400 font-medium">
                                    🧠 {activeSelectedTraffic.reasoning_tokens.toLocaleString()} CoT reasoning
                                  </div>
                                ) : (
                                  <div className="text-[10px] text-slate-500">Direct response</div>
                                )}
                              </div>

                              {/* Cache Savings */}
                              <div className="p-2 bg-slate-900/70 rounded-lg border border-white/5 space-y-1">
                                <div className="text-[10px] text-slate-400">EST. CACHE SAVINGS</div>
                                <div className="text-emerald-400 font-bold text-sm">
                                  {activeSelectedTraffic.cache_savings_usd && activeSelectedTraffic.cache_savings_usd > 0
                                    ? `$${activeSelectedTraffic.cache_savings_usd.toFixed(4)}`
                                    : '$0.0000'}
                                </div>
                                <div className="text-[10px] text-slate-500">
                                  {activeSelectedTraffic.cached_tokens && activeSelectedTraffic.prompt_tokens > 0
                                    ? `${((activeSelectedTraffic.cached_tokens / activeSelectedTraffic.prompt_tokens) * 100).toFixed(0)}% discounted`
                                    : '0% discounted'}
                                </div>
                              </div>

                              {/* Total Spent */}
                              <div className="p-2 bg-slate-900/70 rounded-lg border border-white/5 space-y-1">
                                <div className="text-[10px] text-slate-400">NET TRANSACTION COST</div>
                                <div className="text-indigo-300 font-bold text-sm">
                                  ${activeSelectedTraffic.cost_usd.toFixed(4)}
                                </div>
                                <div className="text-[10px] text-slate-500">
                                  {activeSelectedTraffic.total_tokens.toLocaleString()} total tokens
                                </div>
                              </div>
                            </div>

                            {/* Token Segment Progress Bar */}
                            {activeSelectedTraffic.total_tokens > 0 && (
                              <div className="space-y-1">
                                <div className="h-2 w-full bg-slate-800 rounded-full overflow-hidden flex">
                                  {activeSelectedTraffic.cached_tokens && activeSelectedTraffic.cached_tokens > 0 && (
                                    <div
                                      style={{
                                        width: `${(activeSelectedTraffic.cached_tokens / activeSelectedTraffic.total_tokens) * 100}%`,
                                      }}
                                      className="bg-emerald-500 h-full"
                                      title={`Cached Prompt: ${activeSelectedTraffic.cached_tokens.toLocaleString()} tokens`}
                                    />
                                  )}
                                  <div
                                    style={{
                                      width: `${
                                        (((activeSelectedTraffic.prompt_tokens - (activeSelectedTraffic.cached_tokens || 0))) /
                                          activeSelectedTraffic.total_tokens) *
                                        100
                                      }%`,
                                    }}
                                    className="bg-indigo-500 h-full"
                                    title={`Fresh Prompt: ${(activeSelectedTraffic.prompt_tokens - (activeSelectedTraffic.cached_tokens || 0)).toLocaleString()} tokens`}
                                  />
                                  {activeSelectedTraffic.reasoning_tokens && activeSelectedTraffic.reasoning_tokens > 0 && (
                                    <div
                                      style={{
                                        width: `${(activeSelectedTraffic.reasoning_tokens / activeSelectedTraffic.total_tokens) * 100}%`,
                                      }}
                                      className="bg-purple-500 h-full"
                                      title={`Reasoning: ${activeSelectedTraffic.reasoning_tokens.toLocaleString()} tokens`}
                                    />
                                  )}
                                  <div
                                    style={{
                                      width: `${
                                        (((activeSelectedTraffic.completion_tokens - (activeSelectedTraffic.reasoning_tokens || 0))) /
                                          activeSelectedTraffic.total_tokens) *
                                        100
                                      }%`,
                                    }}
                                    className="bg-cyan-400 h-full"
                                    title={`Output Content: ${(activeSelectedTraffic.completion_tokens - (activeSelectedTraffic.reasoning_tokens || 0)).toLocaleString()} tokens`}
                                  />
                                </div>
                                <div className="flex items-center justify-between text-[10px] text-slate-500 font-mono pt-0.5">
                                  <span className="flex items-center gap-1">
                                    <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" /> Cached Prompt
                                  </span>
                                  <span className="flex items-center gap-1">
                                    <span className="h-1.5 w-1.5 rounded-full bg-indigo-500" /> Fresh Prompt
                                  </span>
                                  <span className="flex items-center gap-1">
                                    <span className="h-1.5 w-1.5 rounded-full bg-purple-500" /> CoT Thinking
                                  </span>
                                  <span className="flex items-center gap-1">
                                    <span className="h-1.5 w-1.5 rounded-full bg-cyan-400" /> Net Output
                                  </span>
                                </div>
                              </div>
                            )}
                          </div>
                        )}

                        {activeSelectedTraffic.system_prompt && (
                          <div className="p-2.5 bg-slate-950/40 rounded-lg border border-white/5 text-xs space-y-1">
                            <div className="text-[10px] font-mono text-slate-500 uppercase">System Prompt Snippet</div>
                            <div className="text-slate-400 text-xs italic">{activeSelectedTraffic.system_prompt}</div>
                          </div>
                        )}
                      </div>
                    )}

                    {trafficInspectorTab === 'response' && (
                      <div className="space-y-3">
                        {/* Response Headers */}
                        {Object.keys(activeSelectedTraffic.response_headers || {}).length > 0 && (
                          <details className="text-xs font-mono bg-slate-950/60 p-2 rounded border border-white/5">
                            <summary className="text-slate-400 cursor-pointer hover:text-slate-200 font-sans text-[11px]">
                              Response Headers ({Object.keys(activeSelectedTraffic.response_headers).length})
                            </summary>
                            <div className="mt-2 space-y-1 text-[11px] text-slate-300">
                              {Object.entries(activeSelectedTraffic.response_headers).map(([k, v]) => (
                                <div key={k} className="flex gap-2">
                                  <span className="text-indigo-400">{k}:</span>
                                  <span className="text-slate-300 break-all">{v}</span>
                                </div>
                              ))}
                            </div>
                          </details>
                        )}

                        {/* Raw Body */}
                        <pre className="p-3 bg-slate-950 rounded-lg border border-white/5 font-mono text-xs text-slate-300 overflow-x-auto max-h-[480px] whitespace-pre-wrap leading-relaxed">
                          {formatJSON(activeSelectedTraffic.response_body)}
                        </pre>
                      </div>
                    )}

                    {trafficInspectorTab === 'request' && (
                      <div className="space-y-3">
                        {/* Request Headers */}
                        {Object.keys(activeSelectedTraffic.request_headers || {}).length > 0 && (
                          <details className="text-xs font-mono bg-slate-950/60 p-2 rounded border border-white/5">
                            <summary className="text-slate-400 cursor-pointer hover:text-slate-200 font-sans text-[11px]">
                              Request Headers ({Object.keys(activeSelectedTraffic.request_headers).length})
                            </summary>
                            <div className="mt-2 space-y-1 text-[11px] text-slate-300">
                              {Object.entries(activeSelectedTraffic.request_headers).map(([k, v]) => (
                                <div key={k} className="flex gap-2">
                                  <span className="text-indigo-400">{k}:</span>
                                  <span className="text-slate-300 break-all">{v}</span>
                                </div>
                              ))}
                            </div>
                          </details>
                        )}

                        {/* Raw Body */}
                        <pre className="p-3 bg-slate-950 rounded-lg border border-white/5 font-mono text-xs text-slate-300 overflow-x-auto max-h-[480px] whitespace-pre-wrap leading-relaxed">
                          {formatJSON(activeSelectedTraffic.request_body)}
                        </pre>
                      </div>
                    )}

                    {trafficInspectorTab === 'chat' && (
                      <div className="space-y-3 max-h-[480px] overflow-y-auto pr-1">
                        {parsedMessages.map((msg: any, mIdx: number) => (
                          <div
                            key={mIdx}
                            className={`p-3 rounded-xl border text-xs space-y-1.5 ${
                              msg.role === 'user'
                                ? 'bg-indigo-950/20 border-indigo-500/30 ml-4'
                                : msg.role === 'assistant'
                                ? 'bg-slate-900 border-white/10 mr-4'
                                : 'bg-slate-950/60 border-white/5'
                            }`}
                          >
                            <div className="flex items-center justify-between text-[10px] font-mono">
                              <span
                                className={`uppercase font-bold ${
                                  msg.role === 'user'
                                    ? 'text-indigo-400'
                                    : msg.role === 'assistant'
                                    ? 'text-emerald-400'
                                    : 'text-amber-400'
                                }`}
                              >
                                {msg.role}
                              </span>
                            </div>
                            <div className="text-slate-200 whitespace-pre-wrap leading-relaxed">
                              {typeof msg.content === 'string'
                                ? msg.content
                                : JSON.stringify(msg.content, null, 2)}
                            </div>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ) : null}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Routes Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {routes.map((route: ProxyRoute) => {
          const fullEndpoint = `${window.location.origin}${route.path_prefix}`;
          return (
            <div key={route.id} className="panel space-y-3 relative group">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className="h-2 w-2 rounded-full bg-emerald-400 ring-4 ring-emerald-500/20" />
                  <span className="font-semibold text-slate-200 text-sm">{route.name}</span>
                </div>
                <span className="text-[10px] px-2 py-0.5 rounded bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 font-mono uppercase">
                  {route.protocol_type}
                </span>
              </div>

              {/* Endpoint URLs */}
              <div className="space-y-1.5 text-xs font-mono">
                <div className="p-2 bg-slate-900/80 rounded border border-white/5 space-y-1">
                  <div className="text-[10px] text-slate-500 flex items-center justify-between">
                    <span>AGENT BASE_URL</span>
                    <button
                      onClick={() => handleCopy(route.id, fullEndpoint)}
                      className="text-indigo-400 hover:text-indigo-300 flex items-center gap-1"
                    >
                      {copiedId === route.id ? <Check className="h-3 w-3 text-emerald-400" /> : <Copy className="h-3 w-3" />}
                      {copiedId === route.id ? 'Copied' : 'Copy'}
                    </button>
                  </div>
                  <div className="text-slate-300 truncate" title={fullEndpoint}>
                    {fullEndpoint}
                  </div>
                </div>

                <div className="flex items-center gap-1.5 text-[11px] text-slate-400">
                  <ArrowRight className="h-3 w-3 text-slate-500 flex-shrink-0" />
                  <span className="truncate text-slate-500" title={route.target_upstream}>
                    {route.target_upstream}
                  </span>
                </div>
              </div>

              {/* Footer */}
              <div className="flex items-center justify-between pt-2 border-t border-white/5 text-xs">
                <div className="flex items-center gap-1.5 text-[11px] text-slate-400 font-mono">
                  <Cpu className="h-3 w-3 text-slate-500" />
                  <span>{route.default_model || 'Any Model'}</span>
                </div>
                <button
                  onClick={() => handleDelete(route.id)}
                  className="opacity-0 group-hover:opacity-100 p-1 text-rose-400 hover:bg-rose-500/10 rounded transition-all"
                  title="Delete Route"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </div>
            </div>
          );
        })}
      </div>

      {/* Add Modal */}
      {isModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4">
          <div className="bg-slate-900 border border-white/10 rounded-xl max-w-lg w-full p-6 shadow-2xl space-y-4 max-h-[90vh] overflow-y-auto">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Network className="h-5 w-5 text-indigo-400" />
                <h3 className="font-semibold text-slate-100">Add AI Gateway Route</h3>
              </div>
              <button onClick={() => setIsModalOpen(false)} className="text-slate-400 hover:text-slate-200 text-sm">
                ✕
              </button>
            </div>

            {/* Dynamic Catalog Provider Quick Select */}
            {discoveredProviders.length > 0 && (
              <div className="space-y-1.5">
                <span className="text-[11px] font-medium text-slate-400">Autofill from Synced Catalog (models.dev):</span>
                <div className="flex flex-wrap gap-1.5 max-h-24 overflow-y-auto">
                  {discoveredProviders.map((p) => (
                    <button
                      key={p.name}
                      type="button"
                      onClick={() => handleSelectCatalogProvider(p.name)}
                      className="px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-200 text-xs border border-white/5 transition-colors"
                    >
                      {p.name} ({p.models.length})
                    </button>
                  ))}
                </div>
              </div>
            )}

            {/* Form */}
            <form onSubmit={handleSaveRoute} className="space-y-3 text-xs">
              <div className="space-y-1">
                <label className="text-slate-400">Route Name</label>
                <input
                  type="text"
                  required
                  placeholder="e.g. Enterprise vLLM Cluster"
                  value={formName}
                  onChange={(e) => setFormName(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1">
                  <label className="text-slate-400">Incoming Path Prefix</label>
                  <input
                    type="text"
                    required
                    placeholder="/proxy/my-llm"
                    value={formPath}
                    onChange={(e) => setFormPath(e.target.value)}
                    className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-slate-400">Protocol Type</label>
                  <select
                    value={formProtocol}
                    onChange={(e) => setFormProtocol(e.target.value as any)}
                    className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                  >
                    <option value="openai-compatible">OpenAI Compatible (/v1/chat/completions)</option>
                    <option value="openai">OpenAI Direct</option>
                    <option value="anthropic">Anthropic Messages (/v1/messages)</option>
                    <option value="gemini">Google Gemini</option>
                    <option value="custom">Custom / Raw Forward</option>
                  </select>
                </div>
              </div>

              <div className="space-y-1">
                <label className="text-slate-400">Target Upstream URL</label>
                <input
                  type="text"
                  required
                  placeholder="https://api.together.xyz/v1 or http://localhost:11434/v1"
                  value={formUpstream}
                  onChange={(e) => setFormUpstream(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div className="space-y-1">
                <label className="text-slate-400">Default Model (Optional)</label>
                <input
                  type="text"
                  list="catalog-models-list"
                  placeholder="e.g. custom-model or select from catalog"
                  value={formModel}
                  onChange={(e) => setFormModel(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                />
                <datalist id="catalog-models-list">
                  {catalog.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name} ({m.provider})
                    </option>
                  ))}
                </datalist>
              </div>

              <div className="flex justify-end gap-2 pt-3 border-t border-white/10">
                <button
                  type="button"
                  onClick={() => setIsModalOpen(false)}
                  className="px-3 py-1.5 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 transition-colors"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={isSubmitting}
                  className="px-4 py-1.5 rounded bg-indigo-600 hover:bg-indigo-500 text-white font-medium transition-colors"
                >
                  {isSubmitting ? 'Saving...' : 'Create Route'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}
