import { type FormEvent, useState } from "react";
import { useNavigate } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { api, artifactPath } from "@/lib/api";
import { useSignedIn } from "@/lib/session";

/**
 * The one place a person makes a row.
 *
 * WHY ONE PLACE AND ONE LIST. A row's answer to "what is this" is written two
 * ways in this store: `kind` under type=memory for 344 rows, and `type` itself
 * for a handful. Both spellings exist, neither is empty, and nothing refused
 * the second one - which is how five rows leaked identity to the top level
 * without anybody deciding it. A create surface that offered both would make
 * that permanent: the day somebody picks "todo as a type" out of a dropdown it
 * stops being five strays and becomes something this project supports. So the
 * list here is CLOSED, it is the resolved types, and there is no control that
 * writes the other level. See the ruling, 01M0ANFYWY.
 *
 * WHAT IS ON THE LIST is what a person writes by hand. A finding arrives from
 * an import and an attachment from an upload; neither is something anybody sits
 * down to compose, and offering them would be offering a shape this door cannot
 * fill in honestly.
 *
 * A DIAGRAM IS NOT WRITTEN HERE, and the option says so rather than being
 * missing: it needs a canvas, /diagrams already opens one, and a second door
 * that made an empty diagram would be a row somebody has to go and find.
 */
const WRITABLE = [
  { type: "todo", what: "work somebody is going to do" },
  { type: "note", what: "something this fabric knows" },
  { type: "report", what: "a document somebody reads on purpose" },
];

export function NewEntity() {
  const signedIn = useSignedIn();
  const navigate = useNavigate();
  const [type, setType] = useState("todo");
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [writing, setWriting] = useState(false);

  const write = async (event: FormEvent) => {
    event.preventDefault();
    if (!title.trim() || writing) return;
    setWriting(true);
    setError(null);
    try {
      const made = await api.writeEntity({ type, title: title.trim(), body });
      // Straight to the row it just made. A create that leaves somebody on the
      // form has written something they now have to go and find, which is the
      // shape of the new-diagram defect this console already had once.
      //
      // Built from what the node answered rather than from what this form
      // typed: the door spells the bucket `memory`, and the row that comes back
      // says its own type, which is the one that will still be right when a
      // door starts writing something else.
      navigate(artifactPath({ project: made.project, type: made.type, id: made.id }) ?? "/");
    } catch (err) {
      setError((err as Error).message);
      setWriting(false);
    }
  };

  if (!signedIn) {
    return (
      <div className="p-6 text-muted-foreground text-sm">paste a token to write anything down</div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col overflow-y-auto p-6">
      <form className="flex w-full max-w-2xl flex-col gap-4" onSubmit={write} data-new-entity="">
        <div className="flex flex-col gap-1">
          <h1 className="font-semibold text-base">new</h1>
          <p className="text-muted-foreground text-xs">
            one row, filed in this project so the rest of the fleet can read it
          </p>
        </div>

        <div className="flex flex-col gap-1 text-xs">
          <span className="text-muted-foreground">what it is</span>
          <Select
            aria-label="what it is"
            data-new-entity-type=""
            value={type}
            onChange={(e) => setType(e.target.value)}
          >
            {WRITABLE.map((w) => (
              <option key={w.type} value={w.type}>
                {w.type} - {w.what}
              </option>
            ))}
          </Select>
        </div>

        <div className="flex flex-col gap-1 text-xs">
          <span className="text-muted-foreground">title</span>
          <Input
            aria-label="title"
            data-new-entity-title=""
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="what somebody would look for it by"
          />
        </div>

        <div className="flex flex-col gap-1 text-xs">
          <span className="text-muted-foreground">body</span>
          <textarea
            aria-label="body"
            data-new-entity-body=""
            value={body}
            onChange={(e) => setBody(e.target.value)}
            rows={10}
            className="rounded border border-border bg-background px-2 py-1 font-mono text-foreground text-xs"
            placeholder="the reasoning, which is the half worth writing down"
          />
        </div>

        {error ? <p className="text-destructive text-xs">{error}</p> : null}

        <div className="flex items-center gap-2">
          <Button type="submit" disabled={!title.trim() || writing} data-new-entity-write="">
            {writing ? "writing…" : "write it"}
          </Button>
          {/*
            Said here rather than left as a missing option: a diagram needs a
            canvas and /diagrams opens one.
          */}
          <span className="text-muted-foreground text-xs">
            a diagram is drawn rather than typed -{" "}
            <a className="underline" href="/diagrams">
              /diagrams
            </a>
          </span>
        </div>
      </form>
    </div>
  );
}
