import { useState, useEffect } from 'react';
import { Settings, Save, ShieldAlert, Cpu, HardDrive, DollarSign, Check, Sliders, BellRing, Trash2, Wrench, X, FolderTree, Plus, Database, Sparkles, FolderPlus, ArrowRight } from 'lucide-react';
import { useSettings, useProjects } from '../hooks/useMetrics';
import type { AppSettings, Project } from '../types';

export function SettingsView() {
  const { data: initialSettings, refetch: refetchSettings } = useSettings();
  const { data: projects = [], refetch: refetchProjects } = useProjects();

  const [activeSubTab, setActiveSubTab] = useState<'projects' | 'watcher' | 'guardrails' | 'webhooks' | 'storage'>('projects');

  // Engine Settings state
  const [debounceMs, setDebounceMs] = useState(250);
  const [thrashingThreshold, setThrashingThreshold] = useState(3);
  const [fragilityCutoff, setFragilityCutoff] = useState(50);
  const [costAlertUSD, setCostAlertUSD] = useState(25);
  const [autoPruneDays, setAutoPruneDays] = useState(90);
  const [defaultProvider, setDefaultProvider] = useState('OpenAI');
  const [slackURL, setSlackURL] = useState('');
  const [discordURL, setDiscordURL] = useState('');
  const [customWebhookURL, setCustomWebhookURL] = useState('');
  const [ignorePatterns, setIgnorePatterns] = useState<string[]>([]);
  const [newPattern, setNewPattern] = useState('');

  const [savedSuccess, setSavedSuccess] = useState(false);
  const [isSaving, setIsSaving] = useState(false);

  // New Project Form state
  const [newProjName, setNewProjName] = useState('');
  const [newProjPath, setNewProjPath] = useState('');
  const [isAddingProj, setIsAddingProj] = useState(false);
  const [projActionMsg, setProjActionMsg] = useState<string | null>(null);

  // Project Edit state
  const [editingProject, setEditingProject] = useState<Record<string, Partial<Project>>>({});

  // DB Maintenance state
  const [vacuumMsg, setVacuumMsg] = useState<string | null>(null);
  const [pruneMsg, setPruneMsg] = useState<string | null>(null);
  const [isVacuuming, setIsVacuuming] = useState(false);
  const [isPruning, setIsPruning] = useState(false);

  useEffect(() => {
    if (initialSettings) {
      setDebounceMs(initialSettings.debounce_ms || 250);
      setThrashingThreshold(initialSettings.thrashing_threshold || 3);
      setFragilityCutoff(initialSettings.fragility_cutoff || 50);
      setCostAlertUSD(initialSettings.cost_alert_usd || 25);
      setAutoPruneDays(initialSettings.auto_prune_days || 90);
      setDefaultProvider(initialSettings.default_provider || 'OpenAI');
      setSlackURL(initialSettings.slack_webhook_url || '');
      setDiscordURL(initialSettings.discord_webhook_url || '');
      setCustomWebhookURL(initialSettings.custom_webhook_url || '');
      setIgnorePatterns(initialSettings.ignore_patterns || ['.git', 'node_modules', 'dist']);
    }
  }, [initialSettings]);

  const handleAddPattern = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && newPattern.trim()) {
      e.preventDefault();
      if (!ignorePatterns.includes(newPattern.trim())) {
        setIgnorePatterns([...ignorePatterns, newPattern.trim()]);
      }
      setNewPattern('');
    }
  };

  const handleRemovePattern = (pattern: string) => {
    setIgnorePatterns(ignorePatterns.filter((p) => p !== pattern));
  };

  const handleSaveSettings = async (e: React.FormEvent) => {
    e.preventDefault();
    setIsSaving(true);
    try {
      const payload: Partial<AppSettings> = {
        debounce_ms: debounceMs,
        thrashing_threshold: thrashingThreshold,
        fragility_cutoff: fragilityCutoff,
        cost_alert_usd: costAlertUSD,
        auto_prune_days: autoPruneDays,
        default_provider: defaultProvider,
        slack_webhook_url: slackURL,
        discord_webhook_url: discordURL,
        custom_webhook_url: customWebhookURL,
        ignore_patterns: ignorePatterns,
      };

      await fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });

      setSavedSuccess(true);
      refetchSettings();
      setTimeout(() => setSavedSuccess(false), 3000);
    } catch (err) {
      console.error(err);
    } finally {
      setIsSaving(false);
    }
  };

  const handleAddProject = async (e: React.FormEvent) => {
    e.preventDefault();
    setIsAddingProj(true);
    try {
      await fetch('/api/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: newProjName, path: newProjPath }),
      });
      setNewProjName('');
      setNewProjPath('');
      setProjActionMsg('Project registered and watching started!');
      refetchProjects();
      setTimeout(() => setProjActionMsg(null), 3000);
    } catch (err) {
      console.error(err);
    } finally {
      setIsAddingProj(false);
    }
  };

  const handleActivateProject = async (id: string) => {
    try {
      await fetch(`/api/projects/${id}/activate`, { method: 'POST' });
      refetchProjects();
      window.location.reload();
    } catch (err) {
      console.error(err);
    }
  };

  const handleRemoveProject = async (id: string) => {
    try {
      await fetch(`/api/projects/${id}`, { method: 'DELETE' });
      refetchProjects();
    } catch (err) {
      console.error(err);
    }
  };

  const handleUpdateProjectFields = async (p: Project) => {
    const edit = editingProject[p.id] || {};
    try {
      await fetch(`/api/projects/${p.id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: edit.name ?? p.name,
          description: edit.description ?? p.description,
          claude_logs_path: edit.claude_logs_path ?? p.claude_logs_path,
          cursor_logs_path: edit.cursor_logs_path ?? p.cursor_logs_path,
          cline_logs_path: edit.cline_logs_path ?? p.cline_logs_path,
          aider_logs_path: edit.aider_logs_path ?? p.aider_logs_path,
          custom_logs_path: edit.custom_logs_path ?? p.custom_logs_path,
        }),
      });
      setProjActionMsg(`Saved & rescanned ${p.name}!`);
      refetchProjects();
      setTimeout(() => setProjActionMsg(null), 3000);
    } catch (err) {
      console.error(err);
    }
  };

  const handleVacuum = async () => {
    setIsVacuuming(true);
    try {
      await fetch('/api/db/vacuum', { method: 'POST' });
      setVacuumMsg('Database optimized and pages reclaimed!');
      setTimeout(() => setVacuumMsg(null), 4000);
    } catch (err) {
      setVacuumMsg('Vacuum failed');
    } finally {
      setIsVacuuming(false);
    }
  };

  const handlePrune = async () => {
    setIsPruning(true);
    try {
      const res = await fetch('/api/db/clear-stale', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ days: 30 }),
      });
      const data = await res.json();
      setPruneMsg(`Deleted ${data.deleted_rows || 0} stale events (> 30 days)`);
      setTimeout(() => setPruneMsg(null), 4000);
    } catch (err) {
      setPruneMsg('Prune failed');
    } finally {
      setIsPruning(false);
    }
  };

  return (
    <div className="space-y-6 max-w-6xl mx-auto">
      {/* Header */}
      <div className="panel flex flex-col md:flex-row items-start md:items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="p-2.5 rounded-xl bg-indigo-500/10 text-indigo-400 border border-indigo-500/20">
            <Settings className="h-6 w-6" />
          </div>
          <div>
            <h2 className="text-base font-semibold tracking-tight text-slate-100">Settings & Project Management</h2>
            <p className="text-xs text-slate-400">
              Manage multi-workspace identities, agent session discovery, guardrails, and system webhooks.
            </p>
          </div>
        </div>

        {/* Sub-Tab Navigation Bar */}
        <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-1 text-xs font-medium">
          <button
            onClick={() => setActiveSubTab('projects')}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition-all ${
              activeSubTab === 'projects' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            <FolderTree className="h-3.5 w-3.5" />
            Projects & Workspaces ({projects.length})
          </button>
          <button
            onClick={() => setActiveSubTab('watcher')}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition-all ${
              activeSubTab === 'watcher' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            <Sliders className="h-3.5 w-3.5" />
            Watcher
          </button>
          <button
            onClick={() => setActiveSubTab('guardrails')}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition-all ${
              activeSubTab === 'guardrails' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            <ShieldAlert className="h-3.5 w-3.5" />
            Guardrails
          </button>
          <button
            onClick={() => setActiveSubTab('webhooks')}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition-all ${
              activeSubTab === 'webhooks' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            <BellRing className="h-3.5 w-3.5" />
            Webhooks
          </button>
          <button
            onClick={() => setActiveSubTab('storage')}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition-all ${
              activeSubTab === 'storage' ? 'bg-accent text-white shadow-sm' : 'text-slate-400 hover:text-white'
            }`}
          >
            <HardDrive className="h-3.5 w-3.5" />
            Storage
          </button>
        </div>
      </div>

      {projActionMsg && (
        <div className="p-3 rounded-lg bg-emerald-500/10 border border-emerald-500/20 text-emerald-300 text-xs flex items-center gap-2 animate-in fade-in">
          <Check className="h-4 w-4" />
          {projActionMsg}
        </div>
      )}

      {/* ---------------------------------------------------- */}
      {/* 1. Projects & Workspaces Tab (Inline, No Modals!)    */}
      {/* ---------------------------------------------------- */}
      {activeSubTab === 'projects' && (
        <div className="space-y-6">
          {/* Add New Project Card */}
          <div className="panel space-y-4 border border-accent/20 bg-slate-900/60">
            <div className="flex items-center gap-2 text-slate-200 font-semibold text-sm">
              <FolderPlus className="h-4 w-4 text-cyan-400" />
              <h3>Register New Workspace / Project Directory</h3>
            </div>

            <form onSubmit={handleAddProject} className="grid grid-cols-1 md:grid-cols-12 gap-3 text-xs">
              <div className="md:col-span-4 space-y-1">
                <label className="text-slate-400">Workspace Name</label>
                <input
                  type="text"
                  required
                  placeholder="e.g. Mobile Client / Backend API"
                  value={newProjName}
                  onChange={(e) => setNewProjName(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-accent"
                />
              </div>

              <div className="md:col-span-6 space-y-1">
                <label className="text-slate-400">Absolute Folder Path (Win / Mac / Linux)</label>
                <input
                  type="text"
                  required
                  placeholder="D:\Codebox\MyProject or /home/user/app"
                  value={newProjPath}
                  onChange={(e) => setNewProjPath(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-accent"
                />
              </div>

              <div className="md:col-span-2 flex items-end">
                <button
                  type="submit"
                  disabled={isAddingProj}
                  className="w-full flex items-center justify-center gap-1.5 px-3 py-1.5 rounded bg-accent hover:bg-accent-hover text-white font-medium transition-colors shadow-md"
                >
                  <Plus className="h-4 w-4" />
                  {isAddingProj ? 'Adding...' : 'Start Watching'}
                </button>
              </div>
            </form>
          </div>

          {/* List of Registered Projects (Full Identity Cards) */}
          <div className="space-y-4">
            {projects.map((p: Project) => {
              const edit = editingProject[p.id] || {};
              const sessions = p.discovered_sessions || {};

              return (
                <div key={p.id} className="panel space-y-4 relative group border border-white/10">
                  {/* Card Top */}
                  <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 border-b border-white/5 pb-3">
                    <div className="flex items-center gap-2.5">
                      <div className="p-2 rounded-lg bg-indigo-500/10 text-indigo-400 border border-indigo-500/20">
                        <FolderTree className="h-4 w-4" />
                      </div>
                      <div>
                        <div className="flex items-center gap-2">
                          <input
                            type="text"
                            value={edit.name ?? p.name}
                            onChange={(e) =>
                              setEditingProject({
                                ...editingProject,
                                [p.id]: { ...edit, name: e.target.value },
                              })
                            }
                            className="font-semibold text-sm text-slate-100 bg-transparent border-b border-dashed border-white/20 focus:border-accent focus:outline-none"
                          />
                          <span className="text-[10px] px-2 py-0.5 rounded-full bg-indigo-500/20 text-indigo-300 font-mono">
                            {p.primary_language || 'Generic'}
                          </span>
                          {p.is_active ? (
                            <span className="text-[10px] px-2 py-0.5 rounded-full bg-emerald-500/20 text-emerald-300 font-mono font-bold">
                              ACTIVE WORKSPACE
                            </span>
                          ) : (
                            <button
                              onClick={() => handleActivateProject(p.id)}
                              className="text-[10px] px-2 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 border border-white/10 transition-colors"
                            >
                              Switch to Active
                            </button>
                          )}
                        </div>
                        <input
                          type="text"
                          placeholder="Project description..."
                          value={edit.description ?? p.description ?? ''}
                          onChange={(e) =>
                            setEditingProject({
                              ...editingProject,
                              [p.id]: { ...edit, description: e.target.value },
                            })
                          }
                          className="text-xs text-slate-400 bg-transparent border-b border-transparent hover:border-white/10 focus:border-accent focus:outline-none w-full mt-0.5"
                        />
                      </div>
                    </div>

                    <button
                      onClick={() => handleRemoveProject(p.id)}
                      className="text-slate-500 hover:text-rose-400 p-1.5 rounded hover:bg-rose-500/10 self-start sm:self-center transition-colors"
                      title="Stop watching workspace"
                    >
                      <Trash2 className="h-4 w-4" />
                    </button>
                  </div>

                  {/* Paths & DB Info */}
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs font-mono">
                    <div className="p-2 bg-slate-950/80 rounded border border-white/5 space-y-1">
                      <div className="text-[10px] text-slate-500 flex items-center gap-1">
                        <FolderTree className="h-3 w-3" /> ROOT WORKSPACE FOLDER
                      </div>
                      <div className="text-slate-300 truncate" title={p.path}>
                        {p.path}
                      </div>
                    </div>

                    <div className="p-2 bg-slate-950/80 rounded border border-white/5 space-y-1">
                      <div className="text-[10px] text-slate-500 flex items-center gap-1">
                        <Database className="h-3 w-3 text-amber-400" /> ISOLATED SQLITE DATABASE
                      </div>
                      <div className="text-slate-300 truncate" title={p.db_path}>
                        {p.db_path}
                      </div>
                    </div>
                  </div>

                  {/* Auto-Discovered Agent Badges */}
                  <div className="space-y-2 pt-1">
                    <div className="text-xs font-semibold text-slate-300 flex items-center gap-1.5">
                      <Sparkles className="h-3.5 w-3.5 text-indigo-400" />
                      <span>Discovered Coding Agent Sessions</span>
                    </div>

                    <div className="grid grid-cols-2 sm:grid-cols-5 gap-2">
                      <div className="p-2 rounded-lg bg-slate-950/60 border border-white/5">
                        <div className="text-[11px] text-slate-400">WrongStack</div>
                        <div className="text-sm font-bold text-rose-400 font-mono">
                          {sessions.wrongstack || 0} <span className="text-[10px] font-normal text-slate-500">sessions</span>
                        </div>
                      </div>

                      <div className="p-2 rounded-lg bg-slate-950/60 border border-white/5">
                        <div className="text-[11px] text-slate-400">Claude Code</div>
                        <div className="text-sm font-bold text-indigo-400 font-mono">
                          {sessions.claude_code || 0} <span className="text-[10px] font-normal text-slate-500">logs</span>
                        </div>
                      </div>

                      <div className="p-2 rounded-lg bg-slate-950/60 border border-white/5">
                        <div className="text-[11px] text-slate-400">Cline / Roo</div>
                        <div className="text-sm font-bold text-emerald-400 font-mono">
                          {sessions.cline || 0} <span className="text-[10px] font-normal text-slate-500">tasks</span>
                        </div>
                      </div>

                      <div className="p-2 rounded-lg bg-slate-950/60 border border-white/5">
                        <div className="text-[11px] text-slate-400">Cursor AI</div>
                        <div className="text-sm font-bold text-cyan-400 font-mono">
                          {sessions.cursor || 0} <span className="text-[10px] font-normal text-slate-500">detected</span>
                        </div>
                      </div>

                      <div className="p-2 rounded-lg bg-slate-950/60 border border-white/5">
                        <div className="text-[11px] text-slate-400">Aider CLI</div>
                        <div className="text-sm font-bold text-amber-400 font-mono">
                          {sessions.aider || 0} <span className="text-[10px] font-normal text-slate-500">history</span>
                        </div>
                      </div>
                    </div>
                  </div>

                  {/* Configurable Session Paths */}
                  <div className="space-y-2 pt-2 border-t border-white/5">
                    <span className="text-[11px] text-slate-400 font-medium block">Agent Session Paths (Auto / Custom):</span>
                    <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-xs font-mono">
                      <div className="space-y-1">
                        <span className="text-[10px] text-slate-500">WrongStack Project Sessions Path</span>
                        <input
                          type="text"
                          value={edit.wrongstack_logs_path ?? p.wrongstack_logs_path ?? ''}
                          placeholder="~/.wrongstack/projects/<slug>/sessions"
                          onChange={(e) =>
                            setEditingProject({
                              ...editingProject,
                              [p.id]: { ...edit, wrongstack_logs_path: e.target.value },
                            })
                          }
                          className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 text-[11px] focus:outline-none focus:border-accent"
                        />
                      </div>

                      <div className="space-y-1">
                        <span className="text-[10px] text-slate-500">Claude Code Path</span>
                        <input
                          type="text"
                          value={edit.claude_logs_path ?? p.claude_logs_path ?? ''}
                          onChange={(e) =>
                            setEditingProject({
                              ...editingProject,
                              [p.id]: { ...edit, claude_logs_path: e.target.value },
                            })
                          }
                          className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 text-[11px] focus:outline-none focus:border-accent"
                        />
                      </div>

                      <div className="space-y-1">
                        <span className="text-[10px] text-slate-500">Aider History Path</span>
                        <input
                          type="text"
                          value={edit.aider_logs_path ?? p.aider_logs_path ?? ''}
                          onChange={(e) =>
                            setEditingProject({
                              ...editingProject,
                              [p.id]: { ...edit, aider_logs_path: e.target.value },
                            })
                          }
                          className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 text-[11px] focus:outline-none focus:border-accent"
                        />
                      </div>

                      <div className="space-y-1">
                        <span className="text-[10px] text-slate-500">Cursor / Workspace Path</span>
                        <input
                          type="text"
                          value={edit.cursor_logs_path ?? p.cursor_logs_path ?? ''}
                          onChange={(e) =>
                            setEditingProject({
                              ...editingProject,
                              [p.id]: { ...edit, cursor_logs_path: e.target.value },
                            })
                          }
                          className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 text-[11px] focus:outline-none focus:border-accent"
                        />
                      </div>

                      <div className="space-y-1 md:col-span-2">
                        <span className="text-[10px] text-slate-500">Custom Log Glob Path</span>
                        <input
                          type="text"
                          value={edit.custom_logs_path ?? p.custom_logs_path ?? ''}
                          placeholder=".myagent/logs/*.json"
                          onChange={(e) =>
                            setEditingProject({
                              ...editingProject,
                              [p.id]: { ...edit, custom_logs_path: e.target.value },
                            })
                          }
                          className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 text-[11px] focus:outline-none focus:border-accent"
                        />
                      </div>
                    </div>

                    <div className="flex justify-end pt-2">
                      <button
                        type="button"
                        onClick={() => handleUpdateProjectFields(p)}
                        className="flex items-center gap-1.5 px-3 py-1.5 rounded bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-medium transition-colors"
                      >
                        <Save className="h-3.5 w-3.5" />
                        Save & Rescan Identity
                      </button>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* ---------------------------------------------------- */}
      {/* 2. Filesystem & Watcher Tab                          */}
      {/* ---------------------------------------------------- */}
      {activeSubTab === 'watcher' && (
        <form onSubmit={handleSaveSettings} className="space-y-6">
          <div className="panel space-y-4">
            <div className="flex items-center justify-between border-b border-white/5 pb-2">
              <div className="flex items-center gap-2 text-slate-200 font-semibold text-sm">
                <Sliders className="h-4 w-4 text-cyan-400" />
                <h3>Filesystem Observer & Debounce</h3>
              </div>
              <button
                type="submit"
                disabled={isSaving}
                className="flex items-center gap-1.5 px-4 py-1.5 rounded bg-accent hover:bg-accent-hover text-white text-xs font-semibold shadow-md transition-colors"
              >
                {savedSuccess ? <Check className="h-4 w-4 text-emerald-300" /> : <Save className="h-4 w-4" />}
                {savedSuccess ? 'Saved!' : 'Save Watcher Config'}
              </button>
            </div>

            <div className="space-y-2">
              <div className="flex items-center justify-between text-xs">
                <label className="text-slate-300 font-medium">Debounce Burst Window</label>
                <span className="font-mono text-cyan-400">{debounceMs} ms</span>
              </div>
              <input
                type="range"
                min="50"
                max="1000"
                step="25"
                value={debounceMs}
                onChange={(e) => setDebounceMs(Number(e.target.value))}
                className="w-full accent-indigo-500 bg-slate-800"
              />
              <p className="text-[11px] text-slate-500">
                Coalesces rapid file writes from AI agents into a single AST event.
              </p>
            </div>

            <div className="space-y-2 pt-2 border-t border-white/5">
              <label className="text-xs text-slate-300 font-medium block">Ignored Directory Patterns</label>
              <div className="flex flex-wrap gap-1.5 max-h-32 overflow-y-auto p-2 bg-slate-900/80 rounded border border-white/5">
                {ignorePatterns.map((pat) => (
                  <span
                    key={pat}
                    className="inline-flex items-center gap-1 text-[11px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300 border border-white/5"
                  >
                    {pat}
                    <button
                      type="button"
                      onClick={() => handleRemovePattern(pat)}
                      className="text-slate-500 hover:text-rose-400"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </span>
                ))}
              </div>
              <input
                type="text"
                placeholder="Type pattern & press Enter (e.g. .venv, tmp)"
                value={newPattern}
                onChange={(e) => setNewPattern(e.target.value)}
                onKeyDown={handleAddPattern}
                className="w-full bg-slate-900/80 border border-white/10 rounded px-2.5 py-1.5 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
              />
            </div>
          </div>
        </form>
      )}

      {/* ---------------------------------------------------- */}
      {/* 3. Guardrails & Cost Tab                             */}
      {/* ---------------------------------------------------- */}
      {activeSubTab === 'guardrails' && (
        <form onSubmit={handleSaveSettings} className="space-y-6">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="panel space-y-4">
              <div className="flex items-center gap-2 text-slate-200 font-semibold text-sm border-b border-white/5 pb-2">
                <ShieldAlert className="h-4 w-4 text-rose-400" />
                <h3>Thrashing & Fragility Guardrails</h3>
              </div>

              <div className="space-y-2">
                <div className="flex items-center justify-between text-xs">
                  <label className="text-slate-300 font-medium">Thrashing Edit Threshold (24h)</label>
                  <span className="font-mono text-rose-400">≥ {thrashingThreshold} edits</span>
                </div>
                <input
                  type="range"
                  min="2"
                  max="10"
                  step="1"
                  value={thrashingThreshold}
                  onChange={(e) => setThrashingThreshold(Number(e.target.value))}
                  className="w-full accent-rose-500 bg-slate-800"
                />
              </div>

              <div className="space-y-2 pt-2 border-t border-white/5">
                <div className="flex items-center justify-between text-xs">
                  <label className="text-slate-300 font-medium">Fragility Health Cutoff</label>
                  <span className="font-mono text-amber-400">&lt; {fragilityCutoff} score</span>
                </div>
                <input
                  type="range"
                  min="20"
                  max="80"
                  step="5"
                  value={fragilityCutoff}
                  onChange={(e) => setFragilityCutoff(Number(e.target.value))}
                  className="w-full accent-amber-500 bg-slate-800"
                />
              </div>
            </div>

            <div className="panel space-y-4">
              <div className="flex items-center gap-2 text-slate-200 font-semibold text-sm border-b border-white/5 pb-2">
                <DollarSign className="h-4 w-4 text-emerald-400" />
                <h3>Spend Limits & Defaults</h3>
              </div>

              <div className="space-y-1.5">
                <label className="text-xs text-slate-300 font-medium block">Daily Spend Alert ($ USD)</label>
                <input
                  type="number"
                  min="1"
                  max="1000"
                  value={costAlertUSD}
                  onChange={(e) => setCostAlertUSD(Number(e.target.value))}
                  className="w-full bg-slate-900/80 border border-white/10 rounded px-2.5 py-1.5 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div className="space-y-1.5 pt-2 border-t border-white/5">
                <label className="text-xs text-slate-300 font-medium block">Default Provider Fallback</label>
                <select
                  value={defaultProvider}
                  onChange={(e) => setDefaultProvider(e.target.value)}
                  className="w-full bg-slate-900/80 border border-white/10 rounded px-2.5 py-1.5 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                >
                  <option value="OpenAI">OpenAI</option>
                  <option value="Anthropic">Anthropic</option>
                  <option value="OpenRouter">OpenRouter</option>
                  <option value="Groq">Groq</option>
                  <option value="DeepSeek">DeepSeek</option>
                  <option value="Ollama">Local Ollama</option>
                </select>
              </div>

              <div className="pt-3">
                <button
                  type="submit"
                  disabled={isSaving}
                  className="w-full flex items-center justify-center gap-1.5 px-4 py-2 rounded bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-semibold shadow-md transition-colors"
                >
                  <Save className="h-4 w-4" />
                  Save Guardrail Rules
                </button>
              </div>
            </div>
          </div>
        </form>
      )}

      {/* ---------------------------------------------------- */}
      {/* 4. Webhooks Tab                                      */}
      {/* ---------------------------------------------------- */}
      {activeSubTab === 'webhooks' && (
        <form onSubmit={handleSaveSettings} className="panel space-y-4">
          <div className="flex items-center justify-between border-b border-white/5 pb-2">
            <div className="flex items-center gap-2 text-slate-200 font-semibold text-sm">
              <BellRing className="h-4 w-4 text-emerald-400" />
              <h3>Real-Time Security & Thrashing Webhooks</h3>
            </div>
            <button
              type="submit"
              disabled={isSaving}
              className="flex items-center gap-1.5 px-4 py-1.5 rounded bg-accent hover:bg-accent-hover text-white text-xs font-semibold shadow-md transition-colors"
            >
              <Save className="h-4 w-4" />
              Save Webhooks
            </button>
          </div>

          <div className="space-y-3 text-xs max-w-2xl">
            <div className="space-y-1">
              <label className="text-slate-300 font-medium block">Slack Incoming Webhook URL</label>
              <input
                type="text"
                placeholder="https://hooks.slack.com/services/..."
                value={slackURL}
                onChange={(e) => setSlackURL(e.target.value)}
                className="w-full bg-slate-900/80 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
              />
            </div>

            <div className="space-y-1">
              <label className="text-slate-300 font-medium block">Discord Webhook URL</label>
              <input
                type="text"
                placeholder="https://discord.com/api/webhooks/..."
                value={discordURL}
                onChange={(e) => setDiscordURL(e.target.value)}
                className="w-full bg-slate-900/80 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
              />
            </div>

            <div className="space-y-1">
              <label className="text-slate-300 font-medium block">Custom Alert Webhook URL (JSON POST)</label>
              <input
                type="text"
                placeholder="https://my-internal-ops.company.com/alerts"
                value={customWebhookURL}
                onChange={(e) => setCustomWebhookURL(e.target.value)}
                className="w-full bg-slate-900/80 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
              />
            </div>
          </div>
        </form>
      )}

      {/* ---------------------------------------------------- */}
      {/* 5. Storage & DB Maintenance Tab                     */}
      {/* ---------------------------------------------------- */}
      {activeSubTab === 'storage' && (
        <div className="panel space-y-4">
          <div className="flex items-center gap-2 text-slate-200 font-semibold text-sm border-b border-white/5 pb-2">
            <HardDrive className="h-4 w-4 text-violet-400" />
            <h3>Database Diagnostics & Maintenance</h3>
          </div>

          <div className="space-y-3 text-xs font-mono max-w-2xl">
            <div className="p-2.5 bg-slate-950/80 rounded border border-white/5 space-y-1">
              <div className="text-[10px] text-slate-500">GLOBAL APP DATABASE PATH</div>
              <div className="text-slate-300 truncate" title={initialSettings?.db_path || '~/.wrongtrace/wrongtrace.db'}>
                {initialSettings?.db_path || '~/.wrongtrace/wrongtrace.db'}
              </div>
            </div>

            <div className="flex items-center gap-3 pt-2">
              <button
                type="button"
                onClick={handleVacuum}
                disabled={isVacuuming}
                className="flex-1 flex items-center justify-center gap-1.5 px-4 py-2 rounded bg-slate-800 hover:bg-slate-700 text-slate-200 text-xs font-medium border border-white/5 transition-colors"
              >
                <Wrench className="h-3.5 w-3.5 text-cyan-400" />
                {isVacuuming ? 'Optimizing...' : 'Vacuum SQLite DB'}
              </button>

              <button
                type="button"
                onClick={handlePrune}
                disabled={isPruning}
                className="flex-1 flex items-center justify-center gap-1.5 px-4 py-2 rounded bg-rose-500/10 hover:bg-rose-500/20 text-rose-300 text-xs font-medium border border-rose-500/20 transition-colors"
              >
                <Trash2 className="h-3.5 w-3.5" />
                {isPruning ? 'Pruning...' : 'Prune Old Events (>30d)'}
              </button>
            </div>

            {vacuumMsg && <div className="text-[11px] text-cyan-300 pt-1">{vacuumMsg}</div>}
            {pruneMsg && <div className="text-[11px] text-emerald-300 pt-1">{pruneMsg}</div>}
          </div>
        </div>
      )}
    </div>
  );
}

