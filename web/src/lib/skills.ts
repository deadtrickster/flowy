import { type Artifact, request } from "@/lib/api";

/**
 * Skills: how-to procedures the fabric keeps, so a thing that was hard once is
 * hard only once.
 *
 * A skill is an ordinary memory artifact of kind `skill` whose body is the
 * procedure, written as GFM markdown. That is the whole model, and it is a
 * ruling rather than an accident: a skill does not need a lifecycle, an owner
 * or a scope of its own - it is read, and every door an agent or a person
 * already uses for memory (the artifact doors, the console, fuse, the kind
 * filter on GET /api/artifacts) reads it with no new surface to learn. The
 * node accepts any type and kind on the artifact write door, so nothing
 * server-side changes to carry a skill; the console half is what makes it a
 * shelf rather than a pile - see routes/Skills.tsx.
 *
 * The first skill is "doing diagrams in flowy" (the procedure formerly held in
 * memory row 01M0NPCHSR8ER071C8TS3G9VV5), filed at the operator's ask that
 * "doing diagrams in flowy" become a skill.
 */
export const SKILL_TYPE = "memory";
export const SKILL_KIND = "skill";

export interface SkillPage {
  artifacts: Artifact[];
}

export const skills = {
  list: () =>
    request<SkillPage>(
      `/api/artifacts?type=${encodeURIComponent(SKILL_TYPE)}&kind=${encodeURIComponent(SKILL_KIND)}&limit=200`,
    ),

  read: (id: string) => request<Artifact>(`/api/artifact/${encodeURIComponent(id)}`),

  /**
   * Write a skill. Creating and updating are the same call because the node's
   * POST /api/artifacts is an upsert keyed on id - passing an existing id you
   * own is the update branch, and the console's own edit on the artifact page
   * writes through the same door.
   */
  write: (opts: { id?: string; title: string; body: string; project?: string | null }) =>
    request<Artifact>("/api/artifacts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...(opts.id ? { id: opts.id } : {}),
        type: SKILL_TYPE,
        kind: SKILL_KIND,
        title: opts.title,
        body: opts.body,
        ...(opts.project === undefined ? {} : { project: opts.project }),
      }),
    }),
};
