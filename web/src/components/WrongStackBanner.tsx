import { useState, useEffect } from 'react';
import { Sparkles, ExternalLink, X, Layers, GitBranch, ArrowRight } from 'lucide-react';

export function WrongStackBanner() {
  const [dismissed, setDismissed] = useState(false);

  useEffect(() => {
    const isDismissed = localStorage.getItem('wrongstack_banner_dismissed') === 'true';
    setDismissed(isDismissed);
  }, []);

  const handleDismiss = () => {
    setDismissed(true);
    localStorage.setItem('wrongstack_banner_dismissed', 'true');
  };

  if (dismissed) {
    return (
      <div className="flex items-center justify-end px-4 sm:px-6 pt-1">
        <button
          onClick={() => {
            setDismissed(false);
            localStorage.removeItem('wrongstack_banner_dismissed');
          }}
          className="text-[11px] text-slate-500 hover:text-cyan-400 font-mono transition-colors flex items-center gap-1"
        >
          <Layers className="h-3 w-3" /> Show WrongStack Ecosystem
        </button>
      </div>
    );
  }

  return (
    <div className="relative overflow-hidden rounded-xl border border-cyan-500/30 bg-gradient-to-r from-slate-950 via-slate-900 to-cyan-950/40 p-3 sm:p-4 shadow-xl shadow-cyan-950/20 backdrop-blur-md">
      {/* Decorative Glow */}
      <div className="absolute -right-10 -top-10 h-32 w-32 rounded-full bg-cyan-500/10 blur-3xl pointer-events-none" />
      <div className="absolute -left-10 -bottom-10 h-32 w-32 rounded-full bg-accent/10 blur-3xl pointer-events-none" />

      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-3 relative z-10">
        <div className="flex items-start sm:items-center gap-3">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-cyan-500/20 to-accent/20 border border-cyan-400/30 text-cyan-400 shadow-inner">
            <Layers className="h-5 w-5 animate-pulse" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <span className="inline-flex items-center gap-1 rounded-md bg-cyan-500/15 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-cyan-300 border border-cyan-400/30">
                <Sparkles className="h-2.5 w-2.5" /> Flagship Platform
              </span>
              <h4 className="text-sm font-bold text-white tracking-tight flex items-center gap-1.5">
                WrongStack <span className="text-slate-400 text-xs font-normal">· Autonomous Multi-Agent Developer Stack</span>
              </h4>
            </div>
            <p className="text-xs text-slate-300 mt-0.5 line-clamp-1 sm:line-clamp-none">
              Orchestrate AI agent swarms, track deep session lineage, and eliminate code rot with native AST telemetry.
            </p>
          </div>
        </div>

        {/* Action Buttons */}
        <div className="flex items-center gap-2 self-end sm:self-auto shrink-0">
          <a
            href="https://github.com/wrongstack/wrongstack"
            target="_blank"
            rel="noreferrer"
            className="flex items-center gap-1.5 rounded-lg bg-cyan-500/20 hover:bg-cyan-500/30 border border-cyan-400/40 px-3 py-1.5 text-xs font-bold text-cyan-300 hover:text-white transition-all shadow-sm"
          >
            <GitBranch className="h-3.5 w-3.5" />
            <span>Discover WrongStack</span>
            <ArrowRight className="h-3.5 w-3.5" />
          </a>

          <button
            onClick={handleDismiss}
            title="Dismiss Announcement"
            className="rounded-lg p-1.5 text-slate-400 hover:bg-white/5 hover:text-slate-200 transition-colors"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );
}
