import { Plus, Pencil, Trash2, Radio } from 'lucide-react';
import type { EventRecord } from '../types';

interface LiveEventFeedProps {
  events: EventRecord[];
  loading: boolean;
}

function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const diff = Math.max(0, Date.now() - t);
  if (diff < 60_000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

function actionIcon(action: EventRecord['action']) {
  switch (action) {
    case 'ADDED':
      return <Plus className="h-3.5 w-3.5" />;
    case 'MODIFIED':
      return <Pencil className="h-3.5 w-3.5" />;
    case 'DELETED':
      return <Trash2 className="h-3.5 w-3.5" />;
  }
}

function actionColor(action: EventRecord['action']) {
  switch (action) {
    case 'ADDED':
      return 'bg-signal-added/15 text-signal-added';
    case 'MODIFIED':
      return 'bg-signal-modified/15 text-signal-modified';
    case 'DELETED':
      return 'bg-signal-deleted/15 text-signal-deleted';
  }
}

export function LiveEventFeed({ events, loading }: LiveEventFeedProps) {
  return (
    <div className="panel">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Radio className="h-4 w-4 text-accent" />
          <h2 className="font-semibold tracking-tight">Live Event Stream</h2>
        </div>
        <span className="text-xs text-slate-500">most recent first</span>
      </div>

      {loading && <div className="text-sm text-slate-500">loading…</div>}

      {!loading && events.length === 0 && (
        <div className="text-sm text-slate-500 py-8 text-center">
          waiting for the first AST transition…
        </div>
      )}

      <ul className="divide-y divide-white/5 max-h-[420px] overflow-y-auto pr-1">
        {events.slice(0, 50).map((e) => (
          <li key={e.event_id} className="py-2 flex items-start gap-3">
            <span className={`chip ${actionColor(e.action)} mt-0.5`}>
              {actionIcon(e.action)}
              {e.action.toLowerCase()}
            </span>
            <div className="min-w-0 flex-1">
              <div className="font-mono text-xs truncate text-slate-200" title={e.node_signature}>
                {e.node_signature}
              </div>
              <div className="font-mono text-[11px] text-slate-500 truncate" title={e.file_path}>
                {e.file_path}
              </div>
            </div>
            <span className="text-[11px] text-slate-500 shrink-0 mt-0.5">{relativeTime(e.event_time)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
