import { useState, useEffect } from 'react';
import { FolderTree, Database, Code2, Bot, Save, RefreshCw, X, Check, ShieldCheck, Sparkles } from 'lucide-react';
import type { Project } from '../types';

interface ProjectIdentityModalProps {
  project: Project;
  isOpen: boolean;
  onClose: () => void;
  onUpdated: () => void;
}

export function ProjectIdentityModal({
  project,
  isOpen,
  onClose,
  onUpdated,
}: ProjectIdentityModalProps) {
  const [name, setName] = useState(project.name);
  const [description, setDescription] = useState(project.description || '');
  const [claudePath, setClaudePath] = useState(project.claude_logs_path || '');
  const [cursorPath, setCursorPath] = useState(project.cursor_logs_path || '');
  const [clinePath, setClinePath] = useState(project.cline_logs_path || '');
  const [aiderPath, setAiderPath] = useState(project.aider_logs_path || '');
  const [customPath, setCustomPath] = useState(project.custom_logs_path || '');

  const [isSaving, setIsSaving] = useState(false);
  const [saveSuccess, setSaveSuccess] = useState(false);

  useEffect(() => {
    setName(project.name);
    setDescription(project.description || '');
    setClaudePath(project.claude_logs_path || '');
    setCursorPath(project.cursor_logs_path || '');
    setClinePath(project.cline_logs_path || '');
    setAiderPath(project.aider_logs_path || '');
    setCustomPath(project.custom_logs_path || '');
  }, [project]);

  if (!isOpen) return null;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setIsSaving(true);
    try {
      await fetch(`/api/projects/${project.id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name,
          description,
          claude_logs_path: claudePath,
          cursor_logs_path: cursorPath,
          cline_logs_path: clinePath,
          aider_logs_path: aiderPath,
          custom_logs_path: customPath,
        }),
      });
      setSaveSuccess(true);
      onUpdated();
      setTimeout(() => setSaveSuccess(false), 2500);
    } catch (err) {
      console.error(err);
    } finally {
      setIsSaving(false);
    }
  };

  const sessions = project.discovered_sessions || {};

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4 animate-in fade-in">
      <div className="bg-slate-900 border border-white/10 rounded-xl max-w-2xl w-full p-6 shadow-2xl space-y-5 max-h-[90vh] overflow-y-auto">
        {/* Header */}
        <div className="flex items-center justify-between border-b border-white/10 pb-3">
          <div className="flex items-center gap-2.5">
            <div className="p-2 rounded-lg bg-cyan-500/10 text-cyan-400 border border-cyan-500/20">
              <FolderTree className="h-5 w-5" />
            </div>
            <div>
              <div className="flex items-center gap-2">
                <h3 className="font-semibold text-slate-100 text-base">{project.name}</h3>
                <span className="text-[10px] px-2 py-0.5 rounded-full bg-indigo-500/20 text-indigo-300 font-mono">
                  {project.primary_language || 'Generic'}
                </span>
                {project.is_active && (
                  <span className="text-[10px] px-2 py-0.5 rounded-full bg-emerald-500/20 text-emerald-300 font-mono">
                    ACTIVE
                  </span>
                )}
              </div>
              <p className="text-xs text-slate-400">Project Identity & Coding Agent Session Discovery</p>
            </div>
          </div>
          <button onClick={onClose} className="text-slate-400 hover:text-slate-200 p-1">
            <X className="h-4 w-4" />
          </button>
        </div>

        {/* Database & Workspace Info Box */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs font-mono">
          <div className="p-2.5 bg-slate-950/80 rounded-lg border border-white/5 space-y-1">
            <div className="text-[10px] text-slate-500 flex items-center gap-1">
              <FolderTree className="h-3 w-3" /> ROOT WORKSPACE FOLDER
            </div>
            <div className="text-slate-300 truncate" title={project.path}>
              {project.path}
            </div>
          </div>

          <div className="p-2.5 bg-slate-950/80 rounded-lg border border-white/5 space-y-1">
            <div className="text-[10px] text-slate-500 flex items-center gap-1">
              <Database className="h-3 w-3 text-amber-400" /> ISOLATED SQLITE DATABASE
            </div>
            <div className="text-slate-300 truncate" title={project.db_path}>
              {project.db_path}
            </div>
          </div>
        </div>

        {/* Auto-Discovered Agent Badges */}
        <div className="space-y-2">
          <div className="text-xs font-semibold text-slate-300 flex items-center gap-1.5">
            <Sparkles className="h-3.5 w-3.5 text-indigo-400" />
            <span>Auto-Discovered Coding Agent Sessions</span>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
            <div className="p-2.5 rounded-lg bg-slate-800/60 border border-white/5 space-y-1">
              <div className="text-[11px] text-slate-400">Claude Code</div>
              <div className="text-base font-bold text-indigo-400 font-mono">
                {sessions.claude_code || 0} <span className="text-[10px] font-normal text-slate-500">logs</span>
              </div>
            </div>

            <div className="p-2.5 rounded-lg bg-slate-800/60 border border-white/5 space-y-1">
              <div className="text-[11px] text-slate-400">Cline / Roo</div>
              <div className="text-base font-bold text-emerald-400 font-mono">
                {sessions.cline || 0} <span className="text-[10px] font-normal text-slate-500">tasks</span>
              </div>
            </div>

            <div className="p-2.5 rounded-lg bg-slate-800/60 border border-white/5 space-y-1">
              <div className="text-[11px] text-slate-400">Cursor AI</div>
              <div className="text-base font-bold text-cyan-400 font-mono">
                {sessions.cursor || 0} <span className="text-[10px] font-normal text-slate-500">detected</span>
              </div>
            </div>

            <div className="p-2.5 rounded-lg bg-slate-800/60 border border-white/5 space-y-1">
              <div className="text-[11px] text-slate-400">Aider CLI</div>
              <div className="text-base font-bold text-amber-400 font-mono">
                {sessions.aider || 0} <span className="text-[10px] font-normal text-slate-500">history</span>
              </div>
            </div>
          </div>
        </div>

        {/* Configuration Form */}
        <form onSubmit={handleSave} className="space-y-3.5 text-xs">
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <label className="text-slate-400">Project Display Name</label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-accent"
              />
            </div>

            <div className="space-y-1">
              <label className="text-slate-400">Project Description</label>
              <input
                type="text"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="e.g. Core Payment Engine"
                className="w-full bg-slate-800 border border-white/10 rounded px-2.5 py-1.5 text-slate-200 font-mono focus:outline-none focus:border-accent"
              />
            </div>
          </div>

          <div className="space-y-1 pt-2 border-t border-white/5">
            <label className="text-slate-300 font-semibold block">Agent Session Paths (Auto / Custom)</label>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-xs font-mono">
              <div className="space-y-1">
                <span className="text-[10px] text-slate-500">Claude Code Sessions Path</span>
                <input
                  type="text"
                  value={claudePath}
                  onChange={(e) => setClaudePath(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 focus:outline-none focus:border-accent text-[11px]"
                />
              </div>

              <div className="space-y-1">
                <span className="text-[10px] text-slate-500">Aider History Log Path</span>
                <input
                  type="text"
                  value={aiderPath}
                  onChange={(e) => setAiderPath(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 focus:outline-none focus:border-accent text-[11px]"
                />
              </div>

              <div className="space-y-1">
                <span className="text-[10px] text-slate-500">Cursor / Workspace Log Path</span>
                <input
                  type="text"
                  value={cursorPath}
                  onChange={(e) => setCursorPath(e.target.value)}
                  className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 focus:outline-none focus:border-accent text-[11px]"
                />
              </div>

              <div className="space-y-1">
                <span className="text-[10px] text-slate-500">Custom Agent Logs / Glob Path</span>
                <input
                  type="text"
                  value={customPath}
                  onChange={(e) => setCustomPath(e.target.value)}
                  placeholder="e.g. .myagent/logs/*.json"
                  className="w-full bg-slate-800 border border-white/10 rounded px-2 py-1 text-slate-300 focus:outline-none focus:border-accent text-[11px]"
                />
              </div>
            </div>
          </div>

          <div className="flex items-center justify-between pt-3 border-t border-white/10">
            {saveSuccess ? (
              <span className="text-emerald-400 text-xs flex items-center gap-1">
                <Check className="h-3.5 w-3.5" /> Saved and sessions rescanned!
              </span>
            ) : <span />}

            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={onClose}
                className="px-3 py-1.5 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 transition-colors"
              >
                Close
              </button>
              <button
                type="submit"
                disabled={isSaving}
                className="flex items-center gap-1 px-4 py-1.5 rounded bg-accent hover:bg-accent-hover text-white font-medium transition-colors"
              >
                <Save className="h-3.5 w-3.5" />
                {isSaving ? 'Saving...' : 'Save & Rescan'}
              </button>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}
