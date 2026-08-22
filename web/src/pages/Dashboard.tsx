import { useEffect, useMemo, useState } from 'react';
import { Navbar } from '../components/Navbar';
import { MetricsOverview } from '../components/MetricsOverview';
import { ThrashingHeatmap } from '../components/ThrashingHeatmap';
import { ModelLeaderboard } from '../components/ModelLeaderboard';
import { LiveEventFeed } from '../components/LiveEventFeed';
import { ROIAnalysis } from '../components/ROIAnalysis';
import { CodeAtlas } from '../components/CodeAtlas';
import { DiffInspectorView } from '../components/DiffInspectorView';
import { AgentSessionsView } from '../components/AgentSessionsView';
import { ProxyRoutingView } from '../components/ProxyRoutingView';
import { SettingsView } from '../components/SettingsView';
import { useHealth, useModels, useOverview, useRecentEvents, useThrashing, useAtlas } from '../hooks/useMetrics';
import { useWebSocket } from '../hooks/useWebSocket';

// Dashboard is the single-page surface for WrongTrace. It loads via TanStack
// Query and is incrementally refreshed by both HTTP polling and a single
// WebSocket subscription. The WebSocket hook supplies incremental updates;
// React Query handles full snapshot refetch on focus / interval.
export function Dashboard() {
  const [activeTab, setActiveTab] = useState<'dashboard' | 'atlas' | 'diffs' | 'sessions' | 'gateway' | 'settings'>('dashboard');

  const overview = useOverview();
  const thrashing = useThrashing();
  const models = useModels();
  const recent = useRecentEvents();
  const atlas = useAtlas();
  const ws = useWebSocket();

  // When the WS receives a code_event we invalidate the recent-events and atlas cache
  // so the live feed and code map update without waiting for polling.
  useEffect(() => {
    if (ws.lastMessage?.type === 'code_event') {
      recent.refetch();
      atlas.refetch();
    }
    if (ws.lastMessage?.type === 'run_reported') {
      overview.refetch();
      atlas.refetch();
    }
  }, [ws.lastMessage, recent, overview, atlas]);

  // Socket path as REPORTED BY THE DAEMON (/api/health socket_path), so a
  // custom --socket is shown correctly on every platform. Falls back to a
  // platform default only while the first health response is in flight.
  const health = useHealth();
  const socketPath =
    health.data?.socket_path ||
    (typeof navigator !== 'undefined' && /win/i.test(navigator.userAgent || '') ? '\\\\.\\pipe\\wrongtrace' : '/tmp/wrongtrace.sock');

  const activeRunCount = overview.data?.active_runs?.length ?? 0;

  return (
    <div className="min-h-full">
      <Navbar
        repo={overview.data?.repo ?? 'wrongtrace'}
        wsConnected={ws.connected}
        agentCount={activeRunCount}
        socketPath={socketPath}
        activeTab={activeTab}
        onTabChange={setActiveTab}
      />

      <main className="mx-auto max-w-7xl px-4 sm:px-6 py-6 space-y-6">
        {activeTab === 'dashboard' && (
          <>
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
          </>
        )}

        {activeTab === 'atlas' && (
          <CodeAtlas
            atlas={atlas.data}
            recentEvents={recent.data}
            loading={atlas.isLoading}
            onRefresh={() => atlas.refetch()}
          />
        )}

        {activeTab === 'diffs' && (
          <DiffInspectorView
            events={recent.data ?? []}
            loading={recent.isLoading}
          />
        )}

        {activeTab === 'sessions' && (
          <AgentSessionsView
            activeRuns={overview.data?.active_runs ?? []}
            models={models.data ?? []}
            overview={overview.data?.overview}
            recentEvents={recent.data ?? []}
            loading={overview.isLoading}
          />
        )}

        {activeTab === 'gateway' && (
          <ProxyRoutingView />
        )}

        {activeTab === 'settings' && (
          <SettingsView />
        )}

        <footer className="text-xs text-slate-500 pt-4 pb-8">
          WrongTrace observes your filesystem and agent telemetry. Restart any
          agent session with <span className="font-mono">wrongtrace start --watch .</span>{' '}
          to begin collecting events.
        </footer>
      </main>
    </div>
  );
}
