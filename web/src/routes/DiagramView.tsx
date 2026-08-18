import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { DrawioEditor } from "@/components/DrawioEditor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { type Artifact, api } from "@/lib/api";
import {
  type EntityRef,
  diagrams,
  entityRefs,
  entityShape,
  withShape,
  xmlOf,
} from "@/lib/diagrams";
import { useSession } from "@/lib/session";
import { useRooms } from "@/lib/unread";

/** How long after the last edit the diagram is written. */
const SAVE_AFTER_MS = 1200;

/**
 * One diagram, open in the editor, with the flowy entities it refers to listed
 * beside it as real links.
 *
 * The panel is the feature, not decoration. A shape carrying flowyType/flowyId
 * is followable inside drawio because it also carries drawio's own `link`, but
 * only if you find the shape first; the panel is the diagram answering "what
 * does this drawing actually point at" without anybody hunting the canvas. It
 * is read out of the xml on every change rather than tracked alongside it, so
 * deleting a shape removes its row and there is no second copy to disagree.
 */
export function DiagramView() {
  const { id = "" } = useParams();
  const { token } = useSession();
  // The node's rooms, so a diagram can be filed in one this file never named.
  const rooms = useRooms();
  const [artifact, setArtifact] = useState<Artifact | null>(null);
  // The title as it is being typed. It is state of its own rather than read
  // off the artifact because the artifact is what the node last accepted, and
  // a box that snapped back to that on every keystroke could not be typed in.
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [refs, setRefs] = useState<EntityRef[]>([]);
  const [saving, setSaving] = useState<"clean" | "pending" | "saving" | "failed">("clean");

  const timer = useRef<number | null>(null);
  const latest = useRef<string>("");

  // What can be dropped on the canvas: the rooms this console knows and the
  // todos this token can read. Both are entities that already have a page, so
  // a shape naming one is a link that goes somewhere.
  const [todos, setTodos] = useState<Artifact[]>([]);
  const [pick, setPick] = useState("");
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (!token || !id) return;
    let stopped = false;
    diagrams
      .read(id)
      .then((found) => {
        if (stopped) return;
        setArtifact(found);
        setName(found.title);
        setRefs(entityRefs(xmlOf(found)));
      })
      .catch((err: Error) => {
        if (!stopped) setError(err.message);
      });
    return () => {
      stopped = true;
    };
  }, [token, id]);

  useEffect(() => {
    if (!token) return;
    let stopped = false;
    api
      .todos()
      .then((page) => {
        if (!stopped) setTodos(page.artifacts ?? []);
      })
      .catch(() => {
        // The picker degrades to rooms only. A diagram that cannot list todos
        // is still a diagram, and an error here is not this page's headline.
      });
    return () => {
      stopped = true;
    };
  }, [token]);

  const save = useCallback(async () => {
    if (!artifact) return;
    setSaving("saving");
    try {
      const written = await diagrams.write({
        id: artifact.id,
        title: artifact.title,
        // latest.current is only set once the editor has volunteered an
        // autosave, so a write that happens before the first edit has to fall
        // back to the document as it was read. Posting the empty string here
        // would be an upsert of a blank body over the drawing.
        xml: latest.current || xmlOf(artifact),
        project: artifact.project,
      });
      setArtifact(written);
      setSaving("clean");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving("failed");
    }
  }, [artifact]);

  /**
   * Give the diagram a name.
   *
   * A diagram can be created without one - the new button on /diagrams makes a
   * drawing whether or not the box beside it was filled in - so the editor is
   * where the name is settled, and without this the default name would be
   * permanent. It is the same door as a save, because POST /api/artifacts is
   * an upsert on the id, and it carries the document so a rename is not a
   * write of a title over a blank body.
   */
  const rename = useCallback(async () => {
    if (!artifact) return;
    const next = name.trim();
    if (!next || next === artifact.title) {
      // Nothing to write, and an emptied box is a rename to nothing rather
      // than a rename: put back what the row actually says.
      setName(artifact.title);
      return;
    }
    setSaving("saving");
    try {
      const written = await diagrams.write({
        id: artifact.id,
        title: next,
        xml: latest.current || xmlOf(artifact),
        project: artifact.project,
      });
      setArtifact(written);
      setName(written.title);
      setSaving("clean");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving("failed");
    }
  }, [artifact, name]);

  // Debounced: drawio fires autosave per edit, and a write per keystroke would
  // be a request storm against a row the node signs each time.
  const onChange = useCallback(
    (xml: string) => {
      latest.current = xml;
      setRefs(entityRefs(xml));
      setSaving("pending");
      if (timer.current !== null) window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => void save(), SAVE_AFTER_MS);
    },
    [save],
  );

  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
    },
    [],
  );

  /**
   * Drop a shape for a flowy entity onto the canvas.
   *
   * The shape is built here rather than in the editor because only this side
   * knows what a flowy entity is. It carries the id, the type and drawio's own
   * `link`, so it is a link in this console AND in drawio's read-only viewer.
   */
  function insert() {
    if (!artifact || !pick) return;
    const [type, rest] = [pick.slice(0, pick.indexOf(":")), pick.slice(pick.indexOf(":") + 1)];
    const todo = todos.find((t) => t.id === rest);
    const base = latest.current || xmlOf(artifact);
    // Stagger, so inserting several does not stack them all in one spot.
    const n = entityRefs(base).length;
    const next = withShape(
      base,
      entityShape({
        cell: `flowy-${type}-${rest}`,
        type,
        id: rest,
        label: type === "room" ? `#${rest}` : todo?.title || rest,
        project: todo?.project ?? artifact.project ?? undefined,
        x: 80 + (n % 3) * 220,
        y: 80 + Math.floor(n / 3) * 100,
      }),
    );
    latest.current = next;
    setArtifact({ ...artifact, body: next });
    setRefs(entityRefs(next));
    setRevision((r) => r + 1);
    setSaving("pending");
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => void save(), SAVE_AFTER_MS);
  }

  if (!token) {
    return <p className="p-6 text-muted-foreground text-sm">paste a token to open this diagram</p>;
  }
  if (error && !artifact) {
    return <p className="p-6 text-destructive text-sm">{error}</p>;
  }
  if (!artifact) {
    return <p className="p-6 text-muted-foreground text-sm">opening the diagram…</p>;
  }

  return (
    <div className="flex h-full flex-col">
      <header className="flex flex-wrap items-center gap-2 border-border border-b px-4 py-2">
        <Link to="/diagrams" className="text-muted-foreground text-xs hover:underline">
          diagrams
        </Link>
        <span className="text-muted-foreground text-xs">/</span>
        <Input
          className="h-7 w-64 font-semibold text-sm"
          aria-label="diagram title"
          data-testid="diagram-title"
          placeholder={artifact.id}
          value={name}
          autoComplete="off"
          onChange={(event) => setName(event.target.value)}
          onBlur={() => void rename()}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              event.currentTarget.blur();
            }
            if (event.key === "Escape") {
              setName(artifact.title);
            }
          }}
        />
        <Badge variant="outline">{artifact.visibility}</Badge>
        <span className="ml-auto text-muted-foreground text-xs" data-save-state={saving}>
          {saving === "clean"
            ? "saved"
            : saving === "pending"
              ? "unsaved changes…"
              : saving === "saving"
                ? "saving…"
                : "could not save"}
        </span>
      </header>

      {error ? (
        <p className="border-border border-b px-4 py-2 text-destructive text-xs">{error}</p>
      ) : null}

      <div className="flex min-h-0 flex-1">
        <DrawioEditor
          className="min-w-0 flex-1"
          xml={xmlOf(artifact)}
          revision={revision}
          onChange={onChange}
        />

        <aside className="flex w-72 shrink-0 flex-col border-border border-l">
          <header className="flex flex-col gap-2 border-border border-b px-3 py-2">
            <h2 className="font-medium text-xs">refers to</h2>
            <p className="text-muted-foreground text-xs">shapes carrying a flowy id, as links</p>
            <div className="flex items-center gap-1">
              <Select
                className="h-8 min-w-0 flex-1"
                aria-label="a flowy entity to put on the canvas"
                value={pick}
                onChange={(event) => setPick(event.target.value)}
              >
                <option value="">add a reference…</option>
                <optgroup label="rooms">
                  {rooms.map((room) => (
                    <option key={room} value={`room:${room}`}>
                      #{room}
                    </option>
                  ))}
                </optgroup>
                <optgroup label="todos">
                  {todos.map((t) => (
                    <option key={t.id} value={`todo:${t.id}`}>
                      {t.title || t.id}
                    </option>
                  ))}
                </optgroup>
              </Select>
              <Button size="sm" disabled={!pick} onClick={insert} data-testid="insert-reference">
                add
              </Button>
            </div>
          </header>
          {refs.length === 0 ? (
            <p className="px-3 py-3 text-muted-foreground text-xs">
              nothing yet. A shape refers to an entity when it carries flowyType and flowyId - edit
              a shape's properties in the editor to add them.
            </p>
          ) : (
            <ul aria-label="referenced entities" className="min-h-0 flex-1 overflow-y-auto">
              {refs.map((r) => (
                <li
                  key={`${r.type}/${r.id}`}
                  data-entity={`${r.type}/${r.id}`}
                  className="border-border border-b px-3 py-2"
                >
                  <Link className="font-medium text-xs hover:underline" to={r.href}>
                    {r.label}
                  </Link>
                  <div className="flex flex-wrap items-center gap-1 pt-1">
                    <Badge variant="secondary">{r.type}</Badge>
                    <span className="text-muted-foreground text-xs">{r.id}</span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </aside>
      </div>
    </div>
  );
}
