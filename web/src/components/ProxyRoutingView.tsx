import { useState, useMemo } from 'react';
import { Network, Plus, Copy, Check, Trash2, Globe, Cpu, Zap, ArrowRight, ShieldCheck } from 'lucide-react';
import type { ProxyRoute } from '../types';
import { useProxyRoutes, useModelCatalog } from '../hooks/useMetrics';

export function ProxyRoutingView() {
  const { data: routes = [], refetch } = useProxyRoutes();
  const { data: catalog = [] } = useModelCatalog();
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [isModalOpen, setIsModalOpen] = useState(false);

  const [formName, setFormName] = useState('');
  const [formPath, setFormPath] = useState('');
  const [formUpstream, setFormUpstream] = useState('');
  const [formProtocol, setFormProtocol] = useState<'openai' | 'openai-compatible' | 'anthropic' | 'gemini' | 'custom'>('openai-compatible');
  const [formModel, setFormModel] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);

  const discoveredProviders = useMemo(() => {
    const provs = new Map<string, { name: string; models: string[] }>();
    catalog.forEach((m) => {
      const p = m.provider || 'Custom';
      if (!provs.has(p)) {
        provs.set(p, { name: p, models: [] });
      }
      provs.get(p)!.models.push(m.id);
    });
    return Array.from(provs.values());
  }, [catalog]);

  const handleCopy = (id: string, text: string) => {
    navigator.clipboard.writeText(text);
    setCopiedId(id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  const handleDelete = async (id: string) => {
    try {
      await fetch(`/api/proxy/routes/${id}`, { method: 'DELETE' });
      refetch();
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
      refetch();
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
    setFormPath(`/proxy/${p.name.toLowerCase().replace(/\s+/g, '-')}`);
    if (p.models.length > 0) {
      setFormModel(p.models[0]);
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="panel flex flex-col md:flex-row items-start md:items-center justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <Network className="h-5 w-5 text-indigo-400" />
            <h2 className="text-base font-semibold tracking-tight">AI Gateway & Dynamic Route Manager</h2>
          </div>
          <p className="text-xs text-slate-400 mt-1">
            Configure universal routing rules for any AI provider (OpenAI, Anthropic, Gemini, Groq, Ollama, DeepSeek). 
            Intercepts live token counts and wire-level costs transparently.
          </p>
        </div>
        <button
          onClick={() => setIsModalOpen(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium transition-colors shadow-lg shadow-indigo-600/20"
        >
          <Plus className="h-3.5 w-3.5" />
          Add Gateway Route
        </button>
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
