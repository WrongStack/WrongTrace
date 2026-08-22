import { Activity, Cpu, Plug, Radio, LayoutDashboard, Boxes, Code2, Bot } from 'lucide-react';

interface NavbarProps {
  repo: string;
  wsConnected: boolean;
  agentCount: number;
  socketPath: string;
  activeTab: 'dashboard' | 'atlas' | 'diffs' | 'sessions';
  onTabChange: (tab: 'dashboard' | 'atlas' | 'diffs' | 'sessions') => void;
}

export function Navbar({
  repo,
  wsConnected,
  agentCount,
  socketPath,
  activeTab,
  onTabChange,
}: NavbarProps) {
  return (
    <header className="sticky top-0 z-30 border-b border-white/10 bg-bg-base/85 backdrop-blur-xl shadow-lg">
      <div className="mx-auto max-w-7xl px-4 sm:px-6 py-3 flex flex-wrap items-center justify-between gap-4">
        {/* Brand */}
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2 text-base sm:text-lg font-bold tracking-tight">
            <span className="inline-flex h-8 w-8 items-center justify-center rounded-lg bg-accent/20 text-accent shadow-inner border border-accent/30">
              <Activity className="h-4 w-4" />
            </span>
            <span>WrongTrace</span>
            <span className="hidden sm:inline text-[11px] font-normal text-slate-400 bg-white/5 px-2 py-0.5 rounded-full border border-white/5">
              agent observability
            </span>
          </div>

          <div className="hidden lg:flex items-center gap-1.5 text-xs text-slate-400 font-mono bg-slate-900/80 px-2.5 py-1 rounded-lg border border-white/5">
            <Cpu className="h-3 w-3 text-cyan-400" />
            <span>{repo}</span>
          </div>
        </div>

        {/* Tab Navigation */}
        <div className="flex items-center bg-slate-900/90 border border-white/10 rounded-xl p-1 shadow-inner">
          <button
            onClick={() => onTabChange('dashboard')}
            className={`flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg transition-all ${
              activeTab === 'dashboard'
                ? 'bg-accent text-white font-medium shadow-md shadow-accent/20'
                : 'text-slate-400 hover:text-white'
            }`}
          >
            <LayoutDashboard className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">Dashboard</span>
          </button>
          <button
            onClick={() => onTabChange('atlas')}
            className={`flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg transition-all ${
              activeTab === 'atlas'
                ? 'bg-accent text-white font-medium shadow-md shadow-accent/20'
                : 'text-slate-400 hover:text-white'
            }`}
          >
            <Boxes className="h-3.5 w-3.5" />
            <span>Code Atlas</span>
          </button>
          <button
            onClick={() => onTabChange('diffs')}
            className={`flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg transition-all ${
              activeTab === 'diffs'
                ? 'bg-accent text-white font-medium shadow-md shadow-accent/20'
                : 'text-slate-400 hover:text-white'
            }`}
          >
            <Code2 className="h-3.5 w-3.5" />
            <span>Diffs & Changes</span>
          </button>
          <button
            onClick={() => onTabChange('sessions')}
            className={`flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg transition-all ${
              activeTab === 'sessions'
                ? 'bg-accent text-white font-medium shadow-md shadow-accent/20'
                : 'text-slate-400 hover:text-white'
            }`}
          >
            <Bot className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">Agent Sessions</span>
          </button>
        </div>

        {/* Live Telemetry Pills */}
        <div className="ml-auto flex items-center gap-3 text-xs">
          <span className="flex items-center gap-1.5 bg-slate-900/80 px-2.5 py-1 rounded-lg border border-white/5">
            <span className={`h-2 w-2 rounded-full ${wsConnected ? 'bg-signal-added animate-pulse' : 'bg-slate-500'}`} />
            <Radio className="h-3.5 w-3.5 text-slate-400" />
            <span className="text-slate-300 font-mono text-[11px]">{wsConnected ? 'LIVE' : 'RECONNECTING'}</span>
          </span>
          <span className="flex items-center gap-1.5 bg-slate-900/80 px-2.5 py-1 rounded-lg border border-white/5">
            <span className={`h-2 w-2 rounded-full ${agentCount > 0 ? 'bg-accent' : 'bg-slate-500'}`} />
            <Plug className="h-3.5 w-3.5 text-slate-400" />
            <span className="text-slate-300 font-mono text-[11px]">{agentCount} agent{agentCount === 1 ? '' : 's'}</span>
          </span>
          <span className="hidden xl:inline text-slate-500 font-mono text-[11px] bg-slate-900/50 px-2 py-0.5 rounded border border-white/5">
            {socketPath}
          </span>
        </div>
      </div>
    </header>
  );
}
