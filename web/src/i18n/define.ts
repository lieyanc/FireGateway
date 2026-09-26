export type MessageTree = { [key: string]: string | MessageTree }

/** Same keys as T, every leaf a string: the shape a translation must match. */
export type MessageShape<T> = {
  [K in keyof T]: T[K] extends string ? string : MessageShape<T[K]>
}

/** Dotted paths of every leaf in T, e.g. "rules.form.name". */
export type MessagePath<T, Prefix extends string = ""> = {
  [K in keyof T & string]: T[K] extends string
    ? `${Prefix}${K}`
    : MessagePath<T[K], `${Prefix}${K}.`>
}[keyof T & string]

/**
 * Declares one namespace of UI strings. English is the source of truth; the
 * Chinese tree must have exactly the same keys, which the compiler enforces.
 * Placeholders use `{name}` syntax.
 */
export function defineMessages<const T extends MessageTree>(messages: {
  en: T
  zh: MessageShape<T>
}) {
  return messages
}
