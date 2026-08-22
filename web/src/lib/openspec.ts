import type { Artifact, OpenspecConflict } from "@/lib/api";

export type { OpenspecConflict };

/**
 * THE ONE PLACE THIS CONSOLE DIGS INTO AN OPENSPEC ROW'S FIELDS, for the same
 * reason lib/todos is that place for the queue's: the shapes are the store's
 * (internal/store/openspec.go and its siblings), and a console that reads them
 * in three files is three copies that disagree the moment the store moves.
 */

/** OpenspecVerdict is fields.openspec.validation as the validate door (p4)
 * caches it. Absent on rows validated by no door yet - the render treats
 * "no verdict" as its own state, never as a green one. */
export interface OpenspecVerdict {
  ok: boolean;
  problems: string[];
  files_hash?: string;
  checked_at?: number;
}

/**
 * openspecFilesOf reads fields.openspec.files off a change row: the markdown
 * files named by path, proposal.md among them. Absent fields is a row this
 * reader cannot dig - null rather than an empty map, so a caller can tell "no
 * files" from "not a change".
 */
export function openspecFilesOf(a: Artifact): Record<string, string> | null {
  const files = dig(a, ["openspec", "files"]);
  if (typeof files !== "object" || files === null || Array.isArray(files)) return null;
  const out: Record<string, string> = {};
  for (const [path, content] of Object.entries(files)) {
    if (typeof content === "string") out[path] = content;
  }
  return out;
}

/** openspecStateOf reads fields.openspec.state: proposed, in-progress,
 * complete or archived - the lifecycle the transition door owns. */
export function openspecStateOf(a: Artifact): string | undefined {
  const state = dig(a, ["openspec", "state"]);
  return typeof state === "string" ? state : undefined;
}

/** openspecVerdictOf reads fields.openspec.validation. Null is an honest
 * "no verdict on the row" - never an ok. */
export function openspecVerdictOf(a: Artifact): OpenspecVerdict | null {
  const verdict = dig(a, ["openspec", "validation"]);
  if (typeof verdict !== "object" || verdict === null || Array.isArray(verdict)) return null;
  const v = verdict as Record<string, unknown>;
  if (typeof v.ok !== "boolean") return null;
  const problems = Array.isArray(v.problems)
    ? v.problems.filter((p): p is string => typeof p === "string")
    : [];
  return {
    ok: v.ok,
    problems,
    ...(typeof v.files_hash === "string" ? { files_hash: v.files_hash } : {}),
    ...(typeof v.checked_at === "number" ? { checked_at: v.checked_at } : {}),
  };
}

/**
 * dig walks a path of keys down an artifact's fields, which arrive as
 * `unknown` because each type owns its own shape. Every hop guards its type:
 * fields is jsonb, not a schema.
 */
function dig(a: Artifact, path: string[]): unknown {
  let cur: unknown = a.fields;
  for (const key of path) {
    if (typeof cur !== "object" || cur === null || Array.isArray(cur)) return undefined;
    cur = (cur as Record<string, unknown>)[key];
  }
  return cur;
}
