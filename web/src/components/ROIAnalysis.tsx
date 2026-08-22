import { Coins } from 'lucide-react';
import type { ModelRow } from '../types';

interface ROIAnalysisProps {
  models: ModelRow[];
}

// ROIAnalysis renders the "True Token ROI" panel: cost per node surviving 14+
// days. A higher value means the model is expensive relative to durable
// output; a lower value is the goal.
export function ROIAnalysis({ models }: ROIAnalysisProps) {
  const sorted = [...models].sort((a, b) => a.cost_per_surviving_node - b.cost_per_surviving_node);
  const totalCost = models.reduce((acc, m) => acc + m.total_cost_usd, 0);
  const totalSurvived = models.reduce((acc, m) => acc + m.total_survived_nodes, 0);
  const blended = totalSurvived > 0 ? totalCost / totalSurvived : 0;

  return (
    <div className="panel">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Coins className="h-4 w-4 text-yellow-400" />
          <h2 className="font-semibold tracking-tight">True Token ROI</h2>
        </div>
        <span className="text-xs text-slate-500">cost per node surviving ≥14 days</span>
      </div>

      {models.length === 0 ? (
        <div className="text-sm text-slate-500 py-8 text-center">
          ROI surfaces once an agent has reported telemetry and nodes have matured past 14 days.
        </div>
      ) : (
        <>
          <div className="mb-4 panel-raised">
            <div className="stat-label">Blended cost per survived node</div>
            <div className="stat-num">${blended.toFixed(4)}</div>
            <div className="text-xs text-slate-400 mt-1">
              ${totalCost.toFixed(2)} total · {totalSurvived.toLocaleString()} survived nodes
            </div>
          </div>

          <table className="w-full text-xs">
            <thead className="text-slate-400">
              <tr>
                <th className="text-left font-medium py-1">Model</th>
                <th className="text-right font-medium py-1">Cost / Survived</th>
                <th className="text-right font-medium py-1">Survived</th>
                <th className="text-right font-medium py-1">Avg Life</th>
              </tr>
            </thead>
            <tbody className="font-mono">
              {sorted.map((m) => (
                <tr key={m.model} className="border-t border-white/5">
                  <td className="py-1.5 text-slate-200">{m.model}</td>
                  <td className="py-1.5 text-right">
                    <span
                      className={
                        m.cost_per_surviving_node < blended * 0.8
                          ? 'text-signal-added'
                          : m.cost_per_surviving_node > blended * 1.5
                          ? 'text-signal-deleted'
                          : 'text-slate-300'
                      }
                    >
                      ${m.cost_per_surviving_node.toFixed(4)}
                    </span>
                  </td>
                  <td className="py-1.5 text-right text-slate-300">{m.total_survived_nodes}</td>
                  <td className="py-1.5 text-right text-slate-400">
                    {m.avg_longevity_days.toFixed(1)}d
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}
