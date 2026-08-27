import { useEffect, useState } from "react";

import { api } from "@/lib/api";
import { attachmentsIn, type ResolvedAttachment } from "@/lib/markdown";

/**
 * The files a body refers to, fetched so the body can be drawn with them in it.
 *
 * WHY A HOOK AND NOT A SWAP AFTERWARDS. renderDocument is synchronous and the
 * bytes are not. The obvious shortcut - render an <img> with a placeholder src
 * and fill it in once the fetch lands - puts a mutation inside a
 * dangerouslySetInnerHTML subtree, which React does not own: it has no idea the
 * nodes changed, and the next render throws the fetched bytes away. So the
 * fetch happens FIRST and the render is a function of what came back.
 *
 * WHAT COMES BACK IS THREE THINGS, NOT TWO. A file that is not in the map yet
 * is still being fetched. A file in the map with `src` is showable. A file in
 * the map with `why` and no `src` was asked for and cannot be shown - the
 * caller may not read it, its bytes are not on this node (store.ErrNoBytes,
 * which is deliberately not ErrNotFound), or it is not a picture at all. The
 * document draws that third case by name, because a dead <img> renders as the
 * same broken glyph for all three and a document is meant to be evidence.
 */
export function useBodyAttachments(body: string): Map<string, ResolvedAttachment> {
  const [files, setFiles] = useState<Map<string, ResolvedAttachment>>(new Map());
  // The ids as one string, so the effect re-runs when the body refers to a
  // different set of files and not when it is merely re-rendered. An array
  // dependency is a new array every render and would fetch on every keystroke.
  const wanted = attachmentsIn(body).join(" ");

  useEffect(() => {
    const ids = wanted.split(" ").filter(Boolean);
    if (ids.length === 0) {
      setFiles(new Map());
      return;
    }
    let live = true;
    void (async () => {
      const got = new Map<string, ResolvedAttachment>();
      await Promise.all(
        ids.map(async (id) => {
          try {
            const page = await api.attachment(id);
            const fields = (page.item?.fields ?? {}) as Record<string, unknown>;
            const type = typeof fields.content_type === "string" ? fields.content_type : "";
            const title = page.item?.title || id;
            if (page.content === null) {
              got.set(id, { src: "", title, why: "its bytes are not on this node" });
              return;
            }
            // THE SNIFFED TYPE, NOT THE CLAIMED ONE. content_type is what the
            // node decided from the bytes; the writer's claim lives beside it
            // and is not what anything renders from.
            if (!type.startsWith("image/")) {
              got.set(id, {
                src: "",
                title,
                why: `it is ${type || "of no type the node could name"}, not a picture`,
              });
              return;
            }
            got.set(id, { src: `data:${type};base64,${page.content}`, title });
          } catch (err) {
            // The read filter answers a row the caller may not see exactly as
            // it answers one that is not there, and this is the right side of
            // that: the reader is told the reference cannot be followed, and
            // not which of the two it was.
            got.set(id, {
              src: "",
              title: id,
              why: err instanceof Error ? err.message : "the node refused to hand it over",
            });
          }
        }),
      );
      if (live) setFiles(got);
    })();
    return () => {
      live = false;
    };
  }, [wanted]);

  return files;
}
