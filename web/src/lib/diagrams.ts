import { type Artifact, request } from "@/lib/api";

/**
 * Diagrams: draw.io documents that live in the fabric, and whose shapes point
 * at real flowy entities.
 *
 * The point of the feature is the second half. A picture of an architecture
 * that names a room, a todo and a report is a picture whose boxes are DEAD - a
 * reader sees "the merge queue" drawn as a rectangle and has to go and find the
 * merge queue. Here a box carries the id of the thing it draws, so it is a link
 * you can follow, and the diagram stays true to a fabric that moves under it.
 *
 * ---------------------------------------------------------------------------
 * PROVISIONAL, AND NOT THIS FILE'S DECISION TO MAKE.
 *
 * How a diagram is persisted - the artifact type and kind, whether the mxfile
 * xml lives in `body` or in `fields`, and what caps it - belongs to the store
 * half of this feature (flowy row 01M08N148B0SVYNWB7JFRFBFY6), which is being
 * built separately. This module writes through POST /api/artifacts, which
 * already accepts any type and kind, so the console half works end to end
 * today; when the store half rules, DIAGRAM_TYPE/DIAGRAM_KIND and bodyOf/xmlOf
 * are the only things here that change.
 *
 * One known hazard to hand to that side rather than paper over: the node feeds
 * `body` to to_tsvector inline on every write, and Postgres refuses a tsvector
 * input over about a megabyte. Diagram xml passes a megabyte easily. So an xml
 * body needs either a cap or an exclusion from the search vector, and until it
 * has one, a large diagram fails the write inside the transaction.
 * ---------------------------------------------------------------------------
 */
export const DIAGRAM_TYPE = "memory";
export const DIAGRAM_KIND = "diagram";

/** An empty drawio document - what a new diagram starts as. */
export const EMPTY_DIAGRAM =
  '<mxfile><diagram id="page1" name="Page-1"><mxGraphModel dx="800" dy="600" grid="1" ' +
  'gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" ' +
  'pageWidth="850" pageHeight="1100" math="0" shadow="0"><root><mxCell id="0"/>' +
  '<mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>';

/** The mxfile xml of a diagram artifact. See the provisional note above. */
export function xmlOf(artifact: Artifact): string {
  return artifact.body || EMPTY_DIAGRAM;
}

/**
 * The flowy entity a shape refers to.
 *
 * `type` and `id` are carried on the shape as our own attributes, which drawio
 * preserves verbatim because it does not know what they are. `href` is drawio's
 * own `link` attribute, which is why the shape is a real anchor in the viewer
 * rather than something only this console could follow.
 */
export interface EntityRef {
  /** The drawio cell id, so a caller can point at the shape. */
  cell: string;
  type: string;
  id: string;
  label: string;
  href: string;
}

/** The attribute names a shape carries. Ours, not drawio's. */
export const FLOWY_TYPE_ATTR = "flowyType";
export const FLOWY_ID_ATTR = "flowyId";
export const FLOWY_PROJECT_ATTR = "flowyProject";

/**
 * Where a flowy entity lives in this console.
 *
 * These are the console's existing paths, not new ones - a diagram that
 * invented its own would be a set of links that rot the first time a route
 * moves. A room and a task have paths of their own; everything else is an
 * artifact and goes through the artifact route.
 */
export function entityHref(type: string, id: string, project?: string): string {
  const safe = encodeURIComponent(id);
  switch (type) {
    case "room":
      return `/chat/${safe}`;
    case "task":
      return `/task/${safe}`;
    default:
      return `/p/${encodeURIComponent(project || "_")}/${encodeURIComponent(type)}/${safe}`;
  }
}

/**
 * Every flowy entity a diagram points at, read back out of the xml.
 *
 * This is a read of the diagram the user just drew, so it is parsed rather than
 * tracked: the editor owns the document and hands back xml, and a list kept
 * alongside it would be a second copy that disagrees the moment somebody
 * deletes a shape. DOMParser rather than a regex because the document is xml
 * and a regex over markup is how an attribute in a label becomes a reference.
 */
export function entityRefs(xml: string): EntityRef[] {
  if (!xml.trim()) return [];
  let doc: Document;
  try {
    doc = new DOMParser().parseFromString(xml, "text/xml");
  } catch {
    return [];
  }
  if (doc.querySelector("parsererror")) return [];

  const found: EntityRef[] = [];
  const seen = new Set<string>();
  for (const node of Array.from(doc.querySelectorAll(`[${FLOWY_TYPE_ATTR}]`))) {
    const type = node.getAttribute(FLOWY_TYPE_ATTR) ?? "";
    const id = node.getAttribute(FLOWY_ID_ATTR) ?? "";
    if (!type || !id) continue;
    const cell = node.getAttribute("id") ?? "";
    // One entity drawn twice is one entity, but each shape keeps its own row
    // only if it is a different entity - the panel lists what the diagram
    // refers to, not how many boxes it used to say it.
    const key = `${type}/${id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    found.push({
      cell,
      type,
      id,
      label: node.getAttribute("label") || id,
      href:
        node.getAttribute("link") ||
        entityHref(type, id, node.getAttribute(FLOWY_PROJECT_ATTR) ?? undefined),
    });
  }
  return found;
}

/**
 * A shape that refers to a flowy entity, as drawio xml.
 *
 * It is a UserObject rather than a plain mxCell because only a UserObject can
 * carry attributes, and the attributes are the whole point. `link` is set to
 * the same path entityHref would give, so the shape is followable in drawio's
 * own read-only viewer without this console being involved.
 */
export function entityShape(opts: {
  cell: string;
  type: string;
  id: string;
  label: string;
  project?: string;
  x: number;
  y: number;
}): string {
  const at = (s: string) =>
    s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  const href = entityHref(opts.type, opts.id, opts.project);
  const project = opts.project ? ` ${FLOWY_PROJECT_ATTR}="${at(opts.project)}"` : "";
  const style = "rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;";
  return `<UserObject label="${at(opts.label)}" link="${at(href)}" ${FLOWY_TYPE_ATTR}="${at(opts.type)}" ${FLOWY_ID_ATTR}="${at(opts.id)}"${project} id="${at(opts.cell)}"><mxCell style="${style}" vertex="1" parent="1"><mxGeometry x="${opts.x}" y="${opts.y}" width="180" height="60" as="geometry"/></mxCell></UserObject>`;
}

/**
 * Put a shape into a diagram, as xml.
 *
 * Text splicing rather than a DOM round-trip on purpose: the document belongs
 * to drawio, and re-serialising it here through DOMParser/XMLSerializer would
 * quietly rewrite parts of it this console does not understand - entity
 * escaping, attribute order, the compressed-diagram case - so the only edit
 * made is the insertion itself. If there is no <root> to insert into, the
 * document is not one this function can safely touch and it is left alone.
 */
export function withShape(xml: string, shape: string): string {
  const at = xml.lastIndexOf("</root>");
  if (at < 0) return xml;
  return xml.slice(0, at) + shape + xml.slice(at);
}

export interface DiagramPage {
  artifacts: Artifact[];
}

export const diagrams = {
  list: () =>
    request<DiagramPage>(
      `/api/artifacts?type=${encodeURIComponent(DIAGRAM_TYPE)}&kind=${encodeURIComponent(DIAGRAM_KIND)}&limit=200`,
    ),

  read: (id: string) => request<Artifact>(`/api/artifact/${encodeURIComponent(id)}`),

  /**
   * Write a diagram. Creating and saving are the same call because the node's
   * POST /api/artifacts is an upsert keyed on id - passing an existing id you
   * own is the update branch. So a save is not a different door from a create,
   * and there is no half-state where a diagram exists but has never been saved.
   */
  write: (opts: { id?: string; title: string; xml: string; project?: string | null }) =>
    request<Artifact>("/api/artifacts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...(opts.id ? { id: opts.id } : {}),
        type: DIAGRAM_TYPE,
        kind: DIAGRAM_KIND,
        title: opts.title,
        body: opts.xml,
        ...(opts.project === undefined ? {} : { project: opts.project }),
      }),
    }),
};
