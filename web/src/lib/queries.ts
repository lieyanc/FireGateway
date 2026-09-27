import {
  keepPreviousData,
  QueryClient,
  queryOptions,
  useQuery,
} from "@tanstack/react-query"

import { api, isApiError } from "@/lib/api"
import type { MetricRange } from "@/lib/types"

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      retry: (failureCount, error) => {
        if (isApiError(error) && error.status > 0 && error.status < 500) {
          return false
        }
        return failureCount < 2
      },
    },
    mutations: { retry: false },
  },
})

export const qk = {
  authState: ["auth", "state"] as const,
  tokens: ["auth", "tokens"] as const,
  version: ["version"] as const,
  overview: ["overview"] as const,
  rules: ["rules"] as const,
  rule: (id: string) => ["rules", "detail", id] as const,
  connections: (rule?: string) => ["connections", rule ?? "all"] as const,
  connectionsAll: ["connections"] as const,
  realtime: (rule?: string) => ["metrics", "realtime", rule ?? "all"] as const,
  history: (range: MetricRange, rule?: string) =>
    ["metrics", "history", range, rule ?? "all"] as const,
  top: (range: MetricRange) => ["metrics", "top", range] as const,
  logLevel: ["logs", "level"] as const,
  dns: ["dns"] as const,
  settings: ["settings"] as const,
  updateStatus: ["update", "status"] as const,
}

export const authStateQuery = queryOptions({
  queryKey: qk.authState,
  queryFn: api.auth.state,
  staleTime: 60_000,
  retry: 1,
})

export function useAuthState() {
  return useQuery(authStateQuery)
}

export function useVersion() {
  return useQuery({
    queryKey: qk.version,
    queryFn: api.system.version,
    staleTime: 5 * 60_000,
  })
}

export function useOverview() {
  return useQuery({
    queryKey: qk.overview,
    queryFn: api.system.overview,
    refetchInterval: 10_000,
  })
}

export const rulesQuery = queryOptions({
  queryKey: qk.rules,
  queryFn: async () => (await api.rules.list()).items,
})

export function useRules() {
  return useQuery(rulesQuery)
}

export function useRule(id: string) {
  return useQuery({
    queryKey: qk.rule(id),
    queryFn: () => api.rules.get(id),
  })
}

export function useConnections(
  rule: string | undefined,
  options: { refetchInterval?: number | false } = {}
) {
  return useQuery({
    queryKey: qk.connections(rule),
    queryFn: async ({ signal }) =>
      (await api.connections.list(rule, signal)).items,
    refetchInterval: options.refetchInterval ?? false,
    placeholderData: keepPreviousData,
    staleTime: 0,
  })
}

export function useRealtimeMetrics(rule?: string) {
  return useQuery({
    queryKey: qk.realtime(rule),
    queryFn: () => api.metrics.realtime(rule),
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
}

export function useHistoryMetrics(range: MetricRange, rule?: string) {
  return useQuery({
    queryKey: qk.history(range, rule),
    queryFn: () => api.metrics.history(range, rule),
    placeholderData: keepPreviousData,
    refetchInterval: range === "1h" ? 60_000 : 5 * 60_000,
  })
}

export function useTopRules(range: MetricRange) {
  return useQuery({
    queryKey: qk.top(range),
    queryFn: async () => (await api.metrics.top(range)).items,
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
  })
}

export function useDnsCache() {
  return useQuery({
    queryKey: qk.dns,
    queryFn: api.dns.list,
    refetchInterval: 10_000,
  })
}

export function useLogLevel() {
  return useQuery({ queryKey: qk.logLevel, queryFn: api.logs.level })
}

export function useSettings() {
  return useQuery({ queryKey: qk.settings, queryFn: api.settings.get })
}

export function useTokens() {
  return useQuery({
    queryKey: qk.tokens,
    queryFn: async () => (await api.auth.tokens()).items,
  })
}
