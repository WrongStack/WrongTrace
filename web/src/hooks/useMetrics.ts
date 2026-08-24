import { useQuery } from '@tanstack/react-query';
import type { FileHealth, HealthResponse, MetricsSnapshot, ModelRow, ThrashingRow, EventRecord, AtlasSnapshot } from '../types';

const base = '/api';

// Small fetch wrapper that adds a JSON content type and a reasonable timeout.
// Throws on non-2xx so React Query can retry / surface errors uniformly.
async function jget<T>(url: string, init?: RequestInit): Promise<T> {
  const r = await fetch(url, { ...init, headers: { Accept: 'application/json', ...(init?.headers ?? {}) } });
  if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
  return (await r.json()) as T;
}

export function useOverview(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<MetricsSnapshot>({
    queryKey: ['overview', projectId || 'active'],
    queryFn: () => jget<MetricsSnapshot>(`${base}/metrics/overview${q}`),
    refetchInterval: 10_000,
  });
}

export function useThrashing(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<ThrashingRow[]>({
    queryKey: ['thrashing', projectId || 'active'],
    queryFn: () => jget<ThrashingRow[]>(`${base}/metrics/thrashing${q}`),
    refetchInterval: 10_000,
  });
}

export function useModels(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<ModelRow[]>({
    queryKey: ['models', projectId || 'active'],
    queryFn: () => jget<ModelRow[]>(`${base}/metrics/models${q}`),
    refetchInterval: 15_000,
  });
}

export function useRecentEvents(projectId?: string | null, limit: number = 500) {
  const pParam = projectId ? `project_id=${encodeURIComponent(projectId)}` : '';
  const lParam = `limit=${limit}`;
  const q = [pParam, lParam].filter(Boolean).join('&');
  return useQuery<EventRecord[]>({
    queryKey: ['recent', projectId || 'active', limit],
    queryFn: () => jget<EventRecord[]>(`${base}/metrics/recent?${q}`),
    refetchInterval: 5_000,
  });
}

export function useHealth() {
  return useQuery<HealthResponse>({
    queryKey: ['health'],
    queryFn: () => jget<HealthResponse>(`${base}/health`),
    // The socket path never changes during a daemon's lifetime; the refetch
    // interval only re-syncs the navbar after a daemon restart.
    staleTime: 60_000,
    refetchInterval: 30_000,
  });
}

export function useFileHealth(path: string | null) {
  return useQuery<FileHealth>({
    queryKey: ['file_health', path],
    queryFn: () => jget<FileHealth>(`${base}/file/health?path=${encodeURIComponent(path ?? '')}`),
    enabled: !!path,
    staleTime: 30_000,
  });
}

export function useAtlas(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<AtlasSnapshot>({
    queryKey: ['atlas', projectId || 'active'],
    queryFn: () => jget<AtlasSnapshot>(`${base}/atlas${q}`),
    refetchInterval: 10_000,
  });
}

export function useModelCatalog() {
  return useQuery<import('../types').ModelInfo[]>({
    queryKey: ['model_catalog'],
    queryFn: () => jget<import('../types').ModelInfo[]>(`${base}/models/catalog`),
    staleTime: 60_000,
  });
}

export function useProviderCatalog() {
  return useQuery<import('../types').ProviderInfo[]>({
    queryKey: ['provider_catalog'],
    queryFn: () => jget<import('../types').ProviderInfo[]>(`${base}/models/providers`),
    staleTime: 60_000,
  });
}

export function useProxyRoutes() {
  return useQuery<import('../types').ProxyRoute[]>({
    queryKey: ['proxy_routes'],
    queryFn: () => jget<import('../types').ProxyRoute[]>(`${base}/proxy/routes`),
    refetchInterval: 10_000,
  });
}

export function useProjects() {
  return useQuery<import('../types').Project[]>({
    queryKey: ['projects'],
    queryFn: () => jget<import('../types').Project[]>(`${base}/projects`),
    refetchInterval: 10_000,
  });
}

export function useProxyTraffic(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').ProxyTrafficRecord[]>({
    queryKey: ['proxy_traffic', projectId || 'active'],
    queryFn: () => jget<import('../types').ProxyTrafficRecord[]>(`${base}/proxy/traffic${q}`),
    refetchInterval: 3_000,
  });
}

export function useSettings() {
  return useQuery<import('../types').AppSettings>({
    queryKey: ['app_settings'],
    queryFn: () => jget<import('../types').AppSettings>(`${base}/settings`),
    staleTime: 60_000,
  });
}

export function useProfilerTraces(limit: number = 50, projectId?: string | null) {
  const pParam = projectId ? `&project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').RuntimeTrace[]>({
    queryKey: ['profiler_traces', limit, projectId || 'active'],
    queryFn: () => jget<import('../types').RuntimeTrace[]>(`${base}/profiler/traces?limit=${limit}${pParam}`),
    refetchInterval: 5_000,
  });
}

export function useProfilerHotspots(limit: number = 25) {
  return useQuery<import('../types').ProfilerHotspot[]>({
    queryKey: ['profiler_hotspots', limit],
    queryFn: () => jget<import('../types').ProfilerHotspot[]>(`${base}/profiler/hotspots?limit=${limit}`),
    refetchInterval: 10_000,
  });
}

export function useProfilerOverview() {
  return useQuery<import('../types').ProfilerOverview>({
    queryKey: ['profiler_overview'],
    queryFn: () => jget<import('../types').ProfilerOverview>(`${base}/profiler/overview`),
    refetchInterval: 10_000,
  });
}

export function useAtlasStatus(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').IndexProgress>({
    queryKey: ['atlas_status', projectId || 'active'],
    queryFn: () => jget<import('../types').IndexProgress>(`${base}/atlas/status${q}`),
    refetchInterval: 3_000,
  });
}

export function useRecentReads(limit: number = 50, projectId?: string | null) {
  const pParam = projectId ? `&project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').FileReadRecord[]>({
    queryKey: ['recent_reads', limit, projectId || 'active'],
    queryFn: () => jget<import('../types').FileReadRecord[]>(`${base}/reads/recent?limit=${limit}${pParam}`),
    refetchInterval: 5_000,
  });
}

export function useFileReadStats(path: string | null) {
  return useQuery<import('../types').FileReadStats>({
    queryKey: ['file_read_stats', path],
    queryFn: () => jget<import('../types').FileReadStats>(`${base}/files/reads?path=${encodeURIComponent(path ?? '')}`),
    enabled: !!path,
    refetchInterval: 10_000,
  });
}

export function useFileReadHeatmap(path: string | null) {
  return useQuery<import('../types').LineReadHeatmap[]>({
    queryKey: ['file_read_heatmap', path],
    queryFn: () => jget<import('../types').LineReadHeatmap[]>(`${base}/files/heatmap?path=${encodeURIComponent(path ?? '')}`),
    enabled: !!path,
    refetchInterval: 10_000,
  });
}

export function useRecentFileEvents(filePath: string | null, limit: number = 50) {
  return useQuery<EventRecord[]>({
    queryKey: ['recent_file_events', filePath, limit],
    queryFn: () => jget<EventRecord[]>(`${base}/metrics/recent?file_path=${encodeURIComponent(filePath ?? '')}&limit=${limit}`),
    enabled: !!filePath,
    refetchInterval: 5_000,
  });
}

export function useSymbolHistory(filePath: string | null, signature: string | null, limit: number = 100) {
  const pParam = filePath ? `path=${encodeURIComponent(filePath)}` : '';
  const sParam = signature ? `signature=${encodeURIComponent(signature)}` : '';
  const queryStr = [pParam, sParam, `limit=${limit}`].filter(Boolean).join('&');

  return useQuery<import('../types').SymbolHistoryRecord[]>({
    queryKey: ['symbol_history', filePath, signature, limit],
    queryFn: () => jget<import('../types').SymbolHistoryRecord[]>(`${base}/symbol/history?${queryStr}`),
    enabled: !!(filePath || signature),
    refetchInterval: 5_000,
  });
}

export function useFileModelActivity(filePath: string | null) {
  return useQuery<import('../types').ModelActivitySummary[]>({
    queryKey: ['file_model_activity', filePath],
    queryFn: () => jget<import('../types').ModelActivitySummary[]>(`${base}/files/activity?path=${encodeURIComponent(filePath ?? '')}`),
    enabled: !!filePath,
    refetchInterval: 10_000,
  });
}

export function useModelFriction(limit: number = 200) {
  return useQuery<import('../types').InterAgentFrictionReport>({
    queryKey: ['model_friction', limit],
    queryFn: () => jget<import('../types').InterAgentFrictionReport>(`${base}/metrics/friction?limit=${limit}`),
    refetchInterval: 5_000,
  });
}

export function useIPCTraffic() {
  return useQuery<import('../types').IPCTrafficRecord[]>({
    queryKey: ['ipc_traffic'],
    queryFn: () => jget<import('../types').IPCTrafficRecord[]>(`${base}/ipc/traffic`),
    refetchInterval: 2_500,
  });
}



