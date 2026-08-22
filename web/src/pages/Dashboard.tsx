import { useEffect, useMemo } from 'react';
import { Navbar } from '../components/Navbar';
import { MetricsOverview } from '../components/MetricsOverview';
import { ThrashingHeatmap } from '../components/ThrashingHeatmap';
import { ModelLeaderboard } from '../components/ModelLeaderboard';
import { LiveEventFeed } from '../components/LiveEventFeed';
import { ROIAnalysis } from '../components/ROIAnalysis';
import { useModels, useOverview, useRecentEvents, useThrashing } from '../hooks/useMetrics';
import { useWebSocket } from '../hooks/useWebSocket';

// Dashboard is the single-page surface for WrongTrace. It loads via TanStack
// Query and is incrementally refreshed by both HTTP polling and a single
// WebSocket subscription. The WebSocket hook supplies incremental updates;
// React Query handles full snapshot refetch on focus / interval.
export function Dashboard() {
  const overview = useOverview();
  const thrashing = useThrashing();
  const models = useModels();
  const recent = useRecentEvents();
  const ws = useWebSocket();

  // When the WS receives a code_event we invalidate the recent-events cache
  // so the live feed updates without waiting for the 5s poll. Other panels
  // keep their own polling cadence to avoid hammering SQLite.
  useEffect(() => {
    if (ws.lastMessage?.type === 'code_event') {
      recent.refetch();
    }
    if (ws.lastMessage?.type === 'run_reported') {
      overview.refetch();
    }
  }, [ws.lastMessage, recent, overview]);

  // Stable socket path for the nav bar (mirrors the daemon default).
  const socketPath = useMemo(() => {
    // Browsers cannot introspect the bound UDS path; the daemon serves a
    // best-effort hint via the API. Until /api/status returns it, we show a
    // sensible fallback.
    return '/tmp/wrongtrace.sock';
  }, []);

  const activeRunCount = overview.data?.active_runs?.length ?? 0;

  return (
    <div className="min-h-full">
      <Navbar
        repo={overview.data?.repo ?? 'wrongtrace'}
        wsConnected={ws.connected}
        agentCount={activeRunCount}
        socketPath={socketPath}
      />

      <main className="mx-auto max-w-7xl px-6 py-6 space-y-6">
        <MetricsOverview
          overview={overview.data?.overview}
          thrashing={thrashing.data ?? []}
          models={models.data ?? []}
          loading={overview.isLoading || thrashing.isLoading || models.isLoading}
        />

        <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
          <ThrashingHeatmap rows={thrashing.data ?? []} loading={thrashing.isLoading} />
          <ModelLeaderboard models={models.data ?? []} loading={models.isLoading} />
        </div>

        <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
          <div className="xl:col-span-2">
            <LiveEventFeed events={recent.data ?? []} loading={recent.isLoading} />
          </div>
          <ROIAnalysis models={models.data ?? []} />
        </div>

        <footer className="text-xs text-slate-500 pt-4 pb-8">
          WrongTrace observes your filesystem and agent telemetry. Restart any
          agent session with <span className="font-mono">wrongtrace start --watch .</span>{' '}
          to begin collecting events.
        </footer>
      </main>
    </div>
  );
}
