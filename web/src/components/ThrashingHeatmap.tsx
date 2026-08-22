import { Flame } from 'lucide-react';
import type { ThrashingRow } from '../types';

interface ThrashingHeatmapProps {
  rows: ThrashingRow[];
  loading: boolean;
}

// intensityFor maps an edit count to a 0..1 bucket that drives the heatmap
// background. The buckets are tuned for the 3-15 edit range we usually see;
// anything beyond is clamped to "critical".
function intensityFor(edits: number): number {
  if (edits <= 2) return 0.1;
  if (edits <= 4) return 0.35;
  if (edits <= 7) return 0.6;
  if (edits <= 12) return 0.85;
  return 1.0;
}

export function ThrashingHeatmap({ rows, loading }: ThrashingHeatmapProps) {
  return (
    <div className="panel">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Flame className="h-4 w-4 text-signal-modified" />
          <h2 className="font-semibold tracking-tight">Thrashing Heatmap</h2>
        </div>
        <span className="text-xs text-slate-500">≥3 edits · 24h window</span>
      </div>

      {loading && <div className="text-sm text-slate-500">loading…</div>}

      {!loading && rows.length === 0 && (
        <div className="text-sm text-slate-500 py-8 text-center">
          no thrashing detected — your code is calm.
        </div>
      )}

      <ul className="divide-y divide-white/5">
        {rows.slice(0, 12).map((r) => {
          const intensity = intensityFor(r.edit_count);
          return (
            <li
              key={`${r.file_path}:${r.signature}`}
              className="py-2.5 grid grid-cols-12 items-center gap-3"
            >
              <div
                className="col-span-1 h-6 rounded-md"
                style={{
                  background: `rgba(239, 68, 68, ${intensity})`,
                }}
                title={`${r.edit_count} edits`}
              />
              <div className="col-span-7 min-w-0">
                <div className="font-mono text-xs truncate text-slate-200" title={r.signature}>
                  {r.signature}
                </div>
                <div className="font-mono text-[11px] text-slate-500 truncate" title={r.file_path}>
                  {r.file_path}
                </div>
              </div>
              <div className="col-span-2 text-right text-xs text-slate-300">
                {r.edit_count} edits
              </div>
              <div className="col-span-2 text-right text-xs text-slate-500">
                {r.window_hours.toFixed(1)}h window
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
