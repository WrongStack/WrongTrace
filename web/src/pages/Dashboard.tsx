import { useEffect, useMemo, useState } from 'react';
import { Navbar } from '../components/Navbar';
import { WrongStackBanner } from '../components/WrongStackBanner';
import { MetricsOverview } from '../components/MetricsOverview';
import { CodeChurnTimeline } from '../components/CodeChurnTimeline';
import { ThrashingHeatmap } from '../components/ThrashingHeatmap';
import { ModelLeaderboard } from '../components/ModelLeaderboard';
import { ModelIntelligenceMatrix } from '../components/ModelIntelligenceMatrix';
import { ModelFrictionMatrix } from '../components/ModelFrictionMatrix';
import { LiveEventFeed } from '../components/LiveEventFeed';
import { ROIAnalysis } from '../components/ROIAnalysis';
import { CodeAtlas } from '../components/CodeAtlas';
import { DiffInspectorView } from '../components/DiffInspectorView';
import { AgentSessionsView } from '../components/AgentSessionsView';
import { ProfilerTracesView } from '../components/ProfilerTracesView';
import { ProxyRoutingView } from '../components/ProxyRoutingView';
import { SettingsView } from '../components/SettingsView';
import { useHealth, useModels, useOverview, useRecentEvents, useThrashing, useAtlas, useProxyTraffic, useProfilerTraces, useProjects } from '../hooks/useMetrics';
import { useWebSocket } from '../hooks/useWebSocket';

// Dashboard is the single-page surface for WrongTrace. It loads via TanStack
// Query and is incrementally refreshed by both HTTP polling and a single
// WebSocket subscription. The WebSocket hook supplies incremental updates;
// React Query handles full snapshot refetch on focus / interval.
export function Dashboard() {
  const [activeTab, setActiveTab] = useState<'dashboard' | 'atlas' | 'diffs' | 'sessions' | 'profiler' | 'gateway' | 'settings'>('dashboard');
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null);

  const { data: projects = [] } = useProjects();

  const currentProject = useMemo(() => {
    if (selectedProjectId) {
      return projects.find((p) => p.id === selectedProjectId) || null;
    }
    return projects.find((p) => p.is_active) || projects[0] || null;
  }, [projects, selectedProjectId]);

  const activeProjId = currentProject?.id ?? null;

  const overview = useOverview(activeProjId);
  const thrashing = useThrashing(activeProjId);
  const models = useModels(activeProjId);
  const recent = useRecentEvents(activeProjId);
  const atlas = useAtlas(activeProjId);
  const proxyTraffic = useProxyTraffic(activeProjId);
  const profilerTraces = useProfilerTraces(50, activeProjId);
  const ws = useWebSocket();

  const { refetch: refetchOverview } = overview;
  const { refetch: refetchThrashing } = thrashing;
  const { refetch: refetchModels } = models;
  const { refetch: refetchRecent } = recent;
  const { refetch: refetchAtlas } = atlas;
  const { refetch: refetchProxyTraffic } = proxyTraffic;
  const { refetch: refetchProfilerTraces } = profilerTraces;

  // When the WS receives a code_event, proxy_traffic, or project_switched we invalidate the caches
  // so the live feed, code map, and telemetry update immediately without polling.
  useEffect(() => {
    if (!ws.lastMessage) return;
    if (ws.lastMessage.type === 'code_event') {
      refetchRecent();
      refetchAtlas();
    } else if (ws.lastMessage.type === 'run_reported') {
      refetchOverview();
      refetchAtlas();
    } else if (ws.lastMessage.type === 'proxy_traffic') {
      refetchProxyTraffic();
      refetchOverview();
    } else if (ws.lastMessage.type === 'profiler_trace') {
      refetchProfilerTraces();
    } else if (ws.lastMessage.type === 'project_switched') {
      refetchOverview();
      refetchThrashing();
      refetchModels();
      refetchRecent();
      refetchAtlas();
      refetchProxyTraffic();
      refetchProfilerTraces();
    }
  }, [
    ws.lastMessage,
    refetchRecent,
    refetchAtlas,
    refetchOverview,
    refetchProxyTraffic,
    refetchProfilerTraces,
    refetchThrashing,
    refetchModels,
  ]);

  // Socket path as REPORTED BY THE DAEMON (/api/health socket_path), so a
  // custom --socket is shown correctly on every platform. Falls back to a
  // platform default only while the first health response is in flight.
  const health = useHealth();
  const socketPath =
    health.data?.socket_path ||
    (typeof navigator !== 'undefined' && /win/i.test(navigator.userAgent || '') ? '\\\\.\\pipe\\wrongtrace' : '/tmp/wrongtrace.sock');

  const activeRunCount = overview.data?.active_runs?.length ?? 0;

  return (
    <div className="min-h-full flex flex-col">
      <Navbar
        repo={overview.data?.repo ?? 'wrongtrace'}
        wsConnected={ws.connected}
        agentCount={activeRunCount}
        socketPath={socketPath}
        activeTab={activeTab}
        onTabChange={setActiveTab}
        selectedProjectId={selectedProjectId}
        onProjectChange={(p) => setSelectedProjectId(p?.id ?? null)}
      />

      <main className="mx-auto max-w-7xl px-4 sm:px-6 py-6 space-y-6 flex-1 w-full">
        {/* WrongStack Ecosystem Flagship Banner */}
        <WrongStackBanner />

        {activeTab === 'dashboard' && (
          <>
            <MetricsOverview
              overview={overview.data?.overview}
              thrashing={thrashing.data ?? []}
              models={models.data ?? []}
              loading={overview.isLoading || thrashing.isLoading || models.isLoading}
              currentProject={currentProject}
            />

            <CodeChurnTimeline
              events={recent.data ?? []}
              loading={recent.isLoading}
            />

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
              <ThrashingHeatmap rows={thrashing.data ?? []} loading={thrashing.isLoading} />
              <ModelLeaderboard models={models.data ?? []} loading={models.isLoading} />
            </div>

            {/* Model Telemetry & Code Durability Intelligence Matrix */}
            <ModelIntelligenceMatrix
              models={models.data ?? []}
              events={recent.data ?? []}
              loading={models.isLoading}
              projectId={activeProjId}
            />

            {/* Inter-Agent Friction & Cross-Thrashing Matrix */}
            <ModelFrictionMatrix />

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
            currentProject={currentProject}
          />
        )}

        {activeTab === 'sessions' && (
          <AgentSessionsView
            activeRuns={overview.data?.active_runs ?? []}
            models={models.data ?? []}
            overview={overview.data?.overview}
            recentEvents={recent.data ?? []}
            loading={overview.isLoading}
            currentProject={currentProject}
          />
        )}

        {activeTab === 'profiler' && (
          <ProfilerTracesView />
        )}

        {activeTab === 'gateway' && (
          <ProxyRoutingView currentProject={currentProject} />
        )}

        {activeTab === 'settings' && (
          <SettingsView />
        )}

        {/* Global Footer with WrongStack Ecosystem Link */}
        <footer className="border-t border-white/5 pt-6 pb-10 text-xs text-slate-500 flex flex-col sm:flex-row items-center justify-between gap-4">
          <div className="flex items-center gap-2">
            <span>Powered by</span>
            <a
              href="https://github.com/wrongstack/wrongstack"
              target="_blank"
              rel="noreferrer"
              className="font-bold text-cyan-400 hover:text-cyan-300 transition-colors"
            >
              WrongStack
            </a>
            <span>· Universal AI Observability & Multi-Agent Engine</span>
          </div>

          <div className="flex items-center gap-4 text-slate-400 font-mono text-[11px]">
            <a href="https://github.com/wrongstack/wrongstack" target="_blank" rel="noreferrer" className="hover:text-white transition-colors">
              WrongStack GitHub ↗
            </a>
            <span className="text-slate-700">|</span>
            <a href="https://github.com/wrongstack/wrongtrace" target="_blank" rel="noreferrer" className="hover:text-white transition-colors">
              WrongTrace Repository
            </a>
            <span className="text-slate-700">|</span>
            <span className="text-slate-500">BUSL-1.1</span>
          </div>
        </footer>
      </main>
    </div>
  );
}
