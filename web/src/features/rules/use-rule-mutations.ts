import { useMutation, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { useI18n } from "@/i18n"
import { api } from "@/lib/api"
import { toastError } from "@/lib/errors"
import { qk } from "@/lib/queries"
import type { BatchAction, RuleView } from "@/lib/types"

/** Writes a fresh RuleView from a mutation response into both caches. */
export function useStoreRule() {
  const queryClient = useQueryClient()
  return (view: RuleView) => {
    queryClient.setQueryData<RuleView>(qk.rule(view.id), view)
    queryClient.setQueryData<RuleView[]>(qk.rules, (old) =>
      old?.map((r) => (r.id === view.id ? view : r))
    )
  }
}

function useInvalidateRules() {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: qk.rules })
    void queryClient.invalidateQueries({ queryKey: qk.overview })
  }
}

/** Enable/disable with an optimistic update of the list and detail caches. */
export function useSetRuleEnabled() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const store = useStoreRule()
  const invalidate = useInvalidateRules()

  return useMutation({
    mutationFn: ({ rule, enabled }: { rule: RuleView; enabled: boolean }) =>
      enabled ? api.rules.enable(rule.id) : api.rules.disable(rule.id),
    onMutate: async ({ rule, enabled }) => {
      await queryClient.cancelQueries({ queryKey: qk.rules })
      const prevList = queryClient.getQueryData<RuleView[]>(qk.rules)
      const prevDetail = queryClient.getQueryData<RuleView>(qk.rule(rule.id))
      const status = enabled ? ("active" as const) : ("inactive" as const)
      queryClient.setQueryData<RuleView[]>(qk.rules, (old) =>
        old?.map((r) => (r.id === rule.id ? { ...r, status } : r))
      )
      if (prevDetail) {
        queryClient.setQueryData<RuleView>(qk.rule(rule.id), { ...prevDetail, status })
      }
      return { prevList, prevDetail }
    },
    onError: (error, { rule }, context) => {
      if (context?.prevList) queryClient.setQueryData(qk.rules, context.prevList)
      if (context?.prevDetail) queryClient.setQueryData(qk.rule(rule.id), context.prevDetail)
      toastError(error, t, t("rules.toast.enableFailed"))
    },
    onSuccess: (view, { enabled }) => {
      store(view)
      toast.success(
        t(enabled ? "rules.toast.enabled" : "rules.toast.disabled", {
          name: view.name || view.id,
        })
      )
    },
    onSettled: invalidate,
  })
}

export function useRestartRule() {
  const { t } = useI18n()
  const store = useStoreRule()
  const invalidate = useInvalidateRules()
  return useMutation({
    mutationFn: (rule: RuleView) => api.rules.restart(rule.id),
    onSuccess: (view) => {
      store(view)
      toast.success(t("rules.toast.restarted", { name: view.name || view.id }))
    },
    onError: (error) => toastError(error, t, t("rules.toast.restartFailed")),
    onSettled: invalidate,
  })
}

export function useDeleteRule() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const invalidate = useInvalidateRules()
  return useMutation({
    mutationFn: (rule: RuleView) => api.rules.remove(rule.id),
    onSuccess: (_data, rule) => {
      queryClient.setQueryData<RuleView[]>(qk.rules, (old) =>
        old?.filter((r) => r.id !== rule.id)
      )
      queryClient.removeQueries({ queryKey: qk.rule(rule.id), exact: true })
      toast.success(t("rules.toast.deleted", { name: rule.name || rule.id }))
    },
    onError: (error) => toastError(error, t, t("rules.toast.deleteFailed")),
    onSettled: invalidate,
  })
}

export function useBatchRules() {
  const { t } = useI18n()
  const invalidate = useInvalidateRules()
  return useMutation({
    mutationFn: ({ action, ids }: { action: BatchAction; ids: string[] }) =>
      api.rules.batch(action, ids),
    onSuccess: (res, { action }) => {
      if (res.ok.length > 0) {
        const key =
          action === "enable"
            ? "rules.toast.batchEnabled"
            : action === "disable"
              ? "rules.toast.batchDisabled"
              : "rules.toast.batchDeleted"
        toast.success(t(key, { count: res.ok.length }))
      }
      if (res.failed.length > 0) {
        toast.error(t("rules.toast.batchFailed", { count: res.failed.length }), {
          description: res.failed
            .slice(0, 5)
            .map((f) => `${f.id}: ${f.message}`)
            .join("\n"),
        })
      }
    },
    onError: (error) => toastError(error, t),
    onSettled: invalidate,
  })
}
