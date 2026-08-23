import { Eye, Bot, Hash, DollarSign, Clock } from 'lucide-react';
import { useFileReadStats, useFileReadHeatmap } from '../hooks/useMetrics';

interface FileReadDetailsProps {
  filePath: string;
}

function fmtUSD(n: number): string {
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
}

export function FileReadDetails({ filePath }: FileReadDetailsProps) {
  const { data: stats, isLoading: statsLoading } = useFileReadStats(filePath);
  const { data: heatmap = [] } = useFileReadHeatmap(filePath);

  if (statsLoading) {
    return (
      <div className="p-4 rounded-xl bg-slate-950/60 border border-white/5 flex items-center justify-center text-xs text-slate-400 gap-2">
        <Eye className="h-4 w-4 animate-pulse text-cyan-400" />
        <span>Loading file read telemetry...</span>
      </div>
    );
  }

  const totalReads = stats?.total_reads ?? 0;
  if (totalReads === 0) {
    return (
      <div className="p-3.5 rounded-xl bg-slate-950/40 border border-white/5 space-y-2">
        <div className="flex items-center justify-between text-xs text-slate-400">
          <span className="flex items-center gap-1.5 font-medium text-slate-300">
            <Eye className="h-3.5 w-3.5 text-cyan-400" />
            File Read & Context Tracing
          </span>
          <span className="text-[10px] font-mono text-slate-500">0 reads</span>
        </div>
        <p className="text-[11px] text-slate-400">
          No AI agent read operations (<code className="text-cyan-300 font-mono">view_file</code>, <code className="text-cyan-300 font-mono">read_file</code>) recorded for this file yet.
        </p>
      </div>
    );
  }

  const modelEntries = Object.entries(stats?.model_breakdown || {});

  return (
    <div className="space-y-3.5 p-3.5 rounded-xl bg-slate-950/70 border border-cyan-500/20 shadow-inner">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-white/10 pb-2.5">
        <div className="flex items-center gap-2">
          <div className="p-1 rounded-lg bg-cyan-500/20 text-cyan-400">
            <Eye className="h-4 w-4" />
          </div>
          <div>
            <h4 className="font-semibold text-xs text-white">File Read & Context Analytics</h4>
            <div className="text-[10px] text-slate-400 font-mono">
              Tracked via Agent Transcripts & Gateway Proxy
            </div>
          </div>
        </div>
        <span className="chip bg-cyan-500/15 text-cyan-300 border border-cyan-500/30 text-[11px] font-mono font-bold">
          {totalReads} Reads
        </span>
      </div>

      {/* Metric Cards Grid */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs">
        <div className="panel-raised p-2">
          <div className="text-slate-400 text-[10px]">Total Reads</div>
          <div className="font-bold text-white text-sm mt-0.5 font-mono">{stats?.total_reads}</div>
        </div>
        <div className="panel-raised p-2">
          <div className="text-slate-400 text-[10px]">Lines Read</div>
          <div className="font-mono text-cyan-300 font-medium mt-0.5">
            {stats?.total_lines_read.toLocaleString()} L
          </div>
        </div>
        <div className="panel-raised p-2">
          <div className="text-slate-400 text-[10px]">Context Tokens</div>
          <div className="font-mono text-purple-300 font-medium mt-0.5">
            {(stats?.total_prompt_tokens ?? 0).toLocaleString()}
          </div>
        </div>
        <div className="panel-raised p-2">
          <div className="text-slate-400 text-[10px]">Context Cost</div>
          <div className="font-mono text-emerald-400 font-medium mt-0.5">
            {fmtUSD(stats?.total_cost_usd ?? 0)}
          </div>
        </div>
      </div>

      {/* Model Breakdown */}
      {modelEntries.length > 0 && (
        <div className="space-y-1.5">
          <div className="text-[11px] font-medium text-slate-300 flex items-center gap-1.5">
            <Bot className="h-3 w-3 text-accent" />
            <span>Models Reading this File</span>
          </div>
          <div className="flex flex-wrap gap-1.5">
            {modelEntries.map(([model, count]) => (
              <span
                key={model}
                className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md bg-slate-900 border border-white/10 text-[10px] font-mono text-slate-200"
              >
                <span className="text-indigo-300">{model}</span>
                <span className="bg-indigo-500/20 text-indigo-400 px-1 rounded font-bold">{count}x</span>
              </span>
            ))}
          </div>
        </div>
      )}

      {/* Line Read Heatmap */}
      {heatmap.length > 0 && (
        <div className="space-y-1.5">
          <div className="text-[11px] font-medium text-slate-300 flex items-center justify-between">
            <span className="flex items-center gap-1.5">
              <Hash className="h-3 w-3 text-cyan-400" />
              <span>Line Range Read Heatmap</span>
            </span>
            <span className="text-[10px] font-mono text-slate-400">{heatmap.length} slices</span>
          </div>
          <div className="max-h-36 overflow-y-auto space-y-1 pr-1 font-mono text-[11px]">
            {heatmap.slice(0, 8).map((hm, idx) => (
              <div
                key={idx}
                className="flex items-center justify-between px-2 py-1 rounded bg-slate-900/80 border border-white/5 hover:border-cyan-500/30 transition-all"
              >
                <span className="text-slate-300">
                  L{hm.start_line} - {hm.end_line > 0 ? `L${hm.end_line}` : 'EOF'}
                </span>
                <div className="flex items-center gap-2">
                  <span className="text-slate-500 text-[10px]">
                    {hm.end_line >= hm.start_line ? `${hm.end_line - hm.start_line + 1} lines` : ''}
                  </span>
                  <span className="chip bg-cyan-500/15 text-cyan-300 border border-cyan-500/30 text-[10px] font-bold px-1.5 py-0.2">
                    {hm.read_count}x read
                  </span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Recent Inspection Stream */}
      {stats?.recent_reads && stats.recent_reads.length > 0 && (
        <div className="space-y-1.5 pt-1 border-t border-white/5">
          <div className="text-[11px] font-medium text-slate-300 flex items-center gap-1.5">
            <Clock className="h-3 w-3 text-slate-400" />
            <span>Recent Inspection History</span>
          </div>
          <div className="max-h-40 overflow-y-auto space-y-1 pr-1">
            {stats.recent_reads.slice(0, 5).map((r) => (
              <div
                key={r.read_id}
                className="p-2 rounded bg-slate-900/60 border border-white/5 text-[10px] space-y-1"
              >
                <div className="flex items-center justify-between text-slate-400">
                  <span className="font-mono text-indigo-300 font-medium flex items-center gap-1">
                    <span className="text-slate-500">{r.agent_name}:</span> {r.model_name}
                  </span>
                  <span className="font-mono text-slate-400">
                    {r.read_time ? new Date(r.read_time).toLocaleTimeString() : 'Just now'}
                  </span>
                </div>
                <div className="flex items-center justify-between text-slate-300 font-mono">
                  <span className="text-cyan-400">
                    {r.tool_name} (L{r.start_line}-L{r.end_line > 0 ? r.end_line : 'EOF'})
                  </span>
                  <span>{r.prompt_tokens ? `${r.prompt_tokens} tokens` : ''}</span>
                </div>
                {r.intent && (
                  <div className="text-slate-400 truncate italic">
                    "{r.intent}"
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
