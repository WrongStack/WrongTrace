import { Eye, Bot, Hash, DollarSign, Clock, GitCommit, FileEdit, Plus, Minus, Layers } from 'lucide-react';
import { useFileReadStats, useFileReadHeatmap, useFileModelActivity } from '../hooks/useMetrics';

interface FileReadDetailsProps {
  filePath: string;
}

function fmtUSD(n: number): string {
  return n.toLocaleString('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 4 });
}

export function FileReadDetails({ filePath }: FileReadDetailsProps) {
  const { data: stats, isLoading: statsLoading } = useFileReadStats(filePath);
  const { data: heatmap = [] } = useFileReadHeatmap(filePath);
  const { data: modelActivity = [] } = useFileModelActivity(filePath);

  if (statsLoading) {
    return (
      <div className="p-4 rounded-xl bg-slate-950/60 border border-white/5 flex items-center justify-center text-xs text-slate-400 gap-2">
        <Eye className="h-4 w-4 animate-pulse text-cyan-400" />
        <span>Loading file read & write telemetry...</span>
      </div>
    );
  }

  const totalReads = stats?.total_reads ?? 0;
  const hasActivity = totalReads > 0 || modelActivity.length > 0;

  if (!hasActivity) {
    return (
      <div className="p-3.5 rounded-xl bg-slate-950/40 border border-white/5 space-y-2">
        <div className="flex items-center justify-between text-xs text-slate-400">
          <span className="flex items-center gap-1.5 font-medium text-slate-300">
            <Eye className="h-3.5 w-3.5 text-cyan-400" />
            File Telemetry (Read vs Write)
          </span>
          <span className="text-[10px] font-mono text-slate-500">0 events</span>
        </div>
        <p className="text-[11px] text-slate-400">
          No AI agent operations (<code className="text-cyan-300 font-mono">view_file</code>, <code className="text-purple-300 font-mono">edit_file</code>) recorded for this file yet.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3.5 p-3.5 rounded-xl bg-slate-950/70 border border-cyan-500/20 shadow-inner">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-white/10 pb-2.5">
        <div className="flex items-center gap-2">
          <div className="p-1 rounded-lg bg-cyan-500/20 text-cyan-400">
            <Eye className="h-4 w-4" />
          </div>
          <div>
            <h4 className="font-semibold text-xs text-white">Model Activity (Read vs Write)</h4>
            <div className="text-[10px] text-slate-400 font-mono">
              Tracked via Agent Transcripts, Watcher & Gateway
            </div>
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          <span className="chip bg-cyan-500/15 text-cyan-300 border border-cyan-500/30 text-[11px] font-mono font-bold">
            {totalReads} Reads
          </span>
          {modelActivity.some((m) => m.write_events > 0) && (
            <span className="chip bg-purple-500/15 text-purple-300 border border-purple-500/30 text-[11px] font-mono font-bold">
              {modelActivity.reduce((acc, m) => acc + m.write_events, 0)} Writes
            </span>
          )}
        </div>
      </div>

      {/* Metric Cards Grid */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs">
        <div className="panel-raised p-2">
          <div className="text-slate-400 text-[10px]">Total Reads</div>
          <div className="font-bold text-white text-sm mt-0.5 font-mono">{stats?.total_reads || 0}</div>
        </div>
        <div className="panel-raised p-2">
          <div className="text-slate-400 text-[10px]">Lines Read</div>
          <div className="font-mono text-cyan-300 font-medium mt-0.5">
            {(stats?.total_lines_read || 0).toLocaleString()} L
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

      {/* Model Read vs Write Breakdown Table */}
      {modelActivity.length > 0 && (
        <div className="space-y-1.5 pt-1">
          <div className="text-[11px] font-medium text-slate-300 flex items-center justify-between">
            <span className="flex items-center gap-1.5">
              <Bot className="h-3 w-3 text-accent" />
              <span>Model Read vs. Write Breakdown</span>
            </span>
            <span className="text-[10px] font-mono text-slate-400">{modelActivity.length} models</span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-[11px] font-mono text-left border-collapse">
              <thead>
                <tr className="border-b border-white/10 text-slate-400 text-[10px]">
                  <th className="py-1 px-1.5">Model</th>
                  <th className="py-1 px-1.5 text-right text-cyan-400">Reads (Lines)</th>
                  <th className="py-1 px-1.5 text-right text-purple-400">Writes (AST)</th>
                  <th className="py-1 px-1.5 text-right text-emerald-400">Net Delta</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/5">
                {modelActivity.map((m) => (
                  <tr key={m.model_name} className="hover:bg-white/5 transition-colors">
                    <td className="py-1.5 px-1.5 font-semibold text-slate-200 truncate max-w-[130px]">
                      {m.model_name}
                    </td>
                    <td className="py-1.5 px-1.5 text-right text-slate-300">
                      {m.read_count > 0 ? (
                        <span>
                          {m.read_count}x <span className="text-slate-500">({m.lines_read}L)</span>
                        </span>
                      ) : (
                        <span className="text-slate-600">-</span>
                      )}
                    </td>
                    <td className="py-1.5 px-1.5 text-right text-slate-300">
                      {m.write_events > 0 ? (
                        <span className="text-purple-300 font-bold">{m.write_events} ops</span>
                      ) : (
                        <span className="text-slate-600">-</span>
                      )}
                    </td>
                    <td className="py-1.5 px-1.5 text-right">
                      {m.lines_added > 0 || m.lines_deleted > 0 ? (
                        <span>
                          <span className="text-emerald-400">+{m.lines_added}</span>
                          <span className="text-slate-500"> / </span>
                          <span className="text-rose-400">-{m.lines_deleted}</span>
                        </span>
                      ) : (
                        <span className="text-slate-600">-</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
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
