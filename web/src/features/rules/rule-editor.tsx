/* eslint-disable react-refresh/only-export-components */
import * as React from "react"

import { RuleFormDialog, type RuleEditorTarget } from "@/features/rules/rule-form-dialog"
import type { Rule, RuleView } from "@/lib/types"

type RuleEditorContextValue = {
  create: () => void
  edit: (rule: Rule) => void
  duplicate: (rule: Rule) => void
}

const RuleEditorContext = React.createContext<RuleEditorContextValue | null>(null)

/** Hosts one rule form dialog that any descendant can open. */
export function RuleEditorProvider({
  children,
  onSaved,
}: {
  children: React.ReactNode
  onSaved?: (view: RuleView, kind: RuleEditorTarget["kind"]) => void
}) {
  const [target, setTarget] = React.useState<RuleEditorTarget | null>(null)

  const value = React.useMemo<RuleEditorContextValue>(
    () => ({
      create: () => setTarget({ kind: "create" }),
      edit: (rule) => setTarget({ kind: "edit", rule }),
      duplicate: (rule) => setTarget({ kind: "duplicate", rule }),
    }),
    []
  )

  return (
    <RuleEditorContext.Provider value={value}>
      {children}
      <RuleFormDialog
        target={target}
        onOpenChange={(open) => !open && setTarget(null)}
        onSaved={onSaved}
      />
    </RuleEditorContext.Provider>
  )
}

export function useRuleEditor() {
  const context = React.useContext(RuleEditorContext)
  if (!context) throw new Error("useRuleEditor must be used within a RuleEditorProvider")
  return context
}
