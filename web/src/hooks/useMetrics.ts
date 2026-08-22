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

export function useOverview() {
  return useQuery<MetricsSnapshot>({
    queryKey: ['overview'],
    queryFn: () => jget<MetricsSnapshot>(`${base}/metrics/overview`),
    refetchInterval: 10_000,
  });
}

export function useThrashing() {
  return useQuery<ThrashingRow[]>({
    queryKey: ['thrashing'],
    queryFn: () => jget<ThrashingRow[]>(`${base}/metrics/thrashing`),
    refetchInterval: 10_000,
  });
}

export function useModels() {
  return useQuery<ModelRow[]>({
    queryKey: ['models'],
    queryFn: () => jget<ModelRow[]>(`${base}/metrics/models`),
    refetchInterval: 15_000,
  });
}

export function useRecentEvents() {
  return useQuery<EventRecord[]>({
    queryKey: ['recent'],
    queryFn: () => jget<EventRecord[]>(`${base}/metrics/recent`),
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

export function useAtlas() {
  return useQuery<AtlasSnapshot>({
    queryKey: ['atlas'],
    queryFn: () => jget<AtlasSnapshot>(`${base}/atlas`),
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
