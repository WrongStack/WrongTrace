import { Activity, Cpu, Plug, Radio } from 'lucide-react';

interface NavbarProps {
  repo: string;
  wsConnected: boolean;
  agentCount: number;
  socketPath: string;
}

export function Navbar({ repo, wsConnected, agentCount, socketPath }: NavbarProps) {
  return (
    <header className="sticky top-0 z-10 border-b border-white/5 bg-bg-base/80 backdrop-blur">
      <div className="mx-auto max-w-7xl px-6 py-4 flex items-center gap-6">
        <div className="flex items-center gap-2 text-lg font-semibold tracking-tight">
          <span className="inline-flex h-8 w-8 items-center justify-center rounded-md bg-accent/20 text-accent">
            <Activity className="h-4 w-4" />
          </span>
          WrongTrace
          <span className="ml-1 text-xs font-normal text-slate-500">agent observability</span>
        </div>

        <div className="hidden md:flex items-center gap-2 text-xs text-slate-400">
          <Cpu className="h-3.5 w-3.5" />
          <span className="font-mono">{repo}</span>
        </div>

        <div className="ml-auto flex items-center gap-4 text-xs">
          <span className="flex items-center gap-1.5">
            <span className={`h-2 w-2 rounded-full ${wsConnected ? 'bg-signal-added' : 'bg-slate-500'}`} />
            <Radio className="h-3.5 w-3.5 text-slate-400" />
            <span className="text-slate-300">{wsConnected ? 'live' : 'reconnecting'}</span>
          </span>
          <span className="flex items-center gap-1.5">
            <span className={`h-2 w-2 rounded-full ${agentCount > 0 ? 'bg-accent' : 'bg-slate-500'}`} />
            <Plug className="h-3.5 w-3.5 text-slate-400" />
            <span className="text-slate-300">{agentCount} agent{agentCount === 1 ? '' : 's'}</span>
          </span>
          <span className="hidden lg:inline text-slate-500 font-mono">{socketPath}</span>
        </div>
      </div>
    </header>
  );
}
