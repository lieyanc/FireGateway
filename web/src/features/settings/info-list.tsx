import { cn } from "@/lib/utils"

export type InfoRow = { label: React.ReactNode; value: React.ReactNode; mono?: boolean }

/** Two-column label/value list for read-only facts. */
export function InfoList({ rows, className }: { rows: InfoRow[]; className?: string }) {
  return (
    <dl
      className={cn(
        "grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-6 gap-y-2 text-sm",
        className
      )}
    >
      {rows.map((row, i) => (
        <div key={i} className="contents">
          <dt className="text-muted-foreground">{row.label}</dt>
          <dd className={cn("min-w-0 break-all", row.mono && "font-mono text-xs leading-5")}>
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}
