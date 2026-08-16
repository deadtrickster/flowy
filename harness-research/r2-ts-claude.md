# r2-ts-claude - tree-sitter for CODE GENERATION

Clones: `/tmp/aider` (Aider-AI/aider @ `5dc9490`), `/tmp/ast-grep` (ast-grep/ast-grep @ `55ff259`),
`/tmp/grok-build` (xai-org/grok-build @ `5163763`), `/tmp/opencode` (sst/opencode @ `4643e65`).
Line numbers are from those checkouts.

## 1. aider's repo map - symbols instead of files

**What.** A ranked, token-budgeted digest of the whole repo's signatures, as one context block. Docs:
the model "can see classes, methods and function signatures from everywhere in the repo"; the map
"only includes the most important identifiers, the ones which are most often referenced by other
portions of the code" (https://aider.chat/docs/repomap.html).

**How, four layers.**

1. *Extraction.* One `-tags.scm` per language, 58 of them (`aider/queries/tree-sitter-language-pack/`),
   capturing exactly two families: `@name.definition.*` and `@name.reference.*` (`aider/repomap.py:319-324`).
   Everything downstream is `Tag(rel_fname, fname, line, name, kind)`, kind `def`/`ref` (`repomap.py:29`).
   A new language is one .scm file, no code.
2. *Fallback.* If a tags file yields defs but no refs (C++ etc.), pygments lexes the file and every
   `Token.Name` becomes a `ref` with `line=-1` (`repomap.py:338-363`) - per-file degradation, not per-repo.
3. *Ranking.* `nx.MultiDiGraph` over files, edge `referencer -> definer` per shared identifier, weight
   `mul * sqrt(num_refs)` (`repomap.py:501-514`). All heuristics in one block (`repomap.py:487-499`):
   x10 if the ident appeared in the user's message, x10 if snake/kebab/camel and >=8 chars, x0.1 if it
   starts with `_`, x0.1 if more than 5 files define it (kills `get`/`run`), x50 if the referencing file
   is in the chat. Then PageRank with a **personalization vector** (`repomap.py:519-525`) seeded from
   chat files, mentioned filenames, and path components matching mentioned idents (`repomap.py:422-445`).
   Rank is redistributed from each source across its out-edges, so the output unit is `(file, ident)`,
   not file (`repomap.py:533-545`).
4. *Fitting.* Binary search on how many ranked tags to render, measuring real tokens each step, accepting
   at 15% error, starting at `max_map_tokens // 25` (`repomap.py:666-706`). Rendering is grep-ast
   TreeContext with tag lines as lines-of-interest, so context gets real source with elided bodies
   (`repomap.py:710-746`), truncated to 100 chars/line (`repomap.py:782`).

Mentioned identifiers are just `re.split(r"\W+", text)` over the current user message
(`aider/coders/base_coder.py:678-682`) - no NLP; the ranking absorbs the noise.

**Copy this.** The whole shape. Especially the def/ref-only capture contract, the x0.1 penalty for idents
defined in >5 files, budget fitting by *measuring* not estimating, and the read-only framing: "Do not
propose changes to these files, treat them as *read-only*. If you need to edit any of these files, ask me
to *add them to the chat* first." (`aider/coders/base_prompts.py:45-48`).

**Skip this.** The diskcache/SQLite tag cache and its recreate-then-fall-back-to-dict recovery path
(`repomap.py:177-215`) - symptom of a cache that corrupts. And `self.tree_cache = dict()` reset per
fitting run (`repomap.py:674`), so ~15 binary-search iterations re-render from scratch.

## 2. ast-grep - YAML rules as deterministic codemods

**What.** Tree-sitter matching with a declarative rule language and a fixer. `SerializableRuleCore
{ rule, constraints, utils, transform }` (`crates/config/src/rule_core.rs:42-53`) inside
`SerializableRuleConfig { core, fix, rewriters, id, language, message, severity, labels, files,
ignores, ... }` (`crates/config/src/rule_config.rs:62-96`).

**The algebra is small and total** (`crates/config/src/rule/mod.rs:47-101`): atomic `pattern`/`kind`/
`regex`/`nth_child`/`range`; relational `inside`/`has`/`precedes`/`follows`; composite `all`/`any`/
`not`/`matches`. That is the entire surface a model must learn, and it is JsonSchema-derived
(`schemas/`) - hand a model the schema and it is structurally unable to emit an unparseable rule.

**Agent-facing loop.** Official `ast-grep-mcp` exposes `dump_syntax_tree` ("Visualize the Abstract
Syntax Tree structure of code snippets"), `test_match_code_rule` ("Test ast-grep YAML rules against code
snippets before applying them to larger codebases"), `find_code`, `find_code_by_rule`
(https://github.com/ast-grep/ast-grep-mcp). Docs prescribe the iteration: decompose, "Identify sub rules
that can be used to match the code", combine, and on failure "revise the rule by removing some sub rules
and debugging unmatching parts" (https://ast-grep.github.io/advanced/prompting.html). New
`crates/outline` is ast-grep growing into aider's job from the rule side - default YAML rules for 13
languages (`crates/outline/src/default_rules/*.yml`) emitting LSP-shaped `SymbolType` with an
`Item`/`Member` split (`crates/outline/src/model.rs:19-57`), deliberately not a graph (`model.rs:5-7`).

**Copy this.** The rule algebra as our *edit* schema, and `dump_syntax_tree` + `test_match_code_rule` as
a model-facing dry-run: a codemod is verified against a snippet before it touches the tree. A YAML rule
is also reviewable on a phone in a way a 400-line diff is not - directly relevant to remote control.

**Skip this.** ast-grep as the *primary* edit path. It is a codemod tool: right for "rename across 200
call sites", wrong for "write this new function".

## 3. AST-anchored edit application vs line/text anchoring

**Node anchoring, concretely.** ast-grep takes the replaced span from the matched node's byte range
`nm.range()`, optionally expanded to a neighbouring node by walking `prev`/`next`
(`crates/config/src/fixer.rs:181-225`). The edit is `Edit { position, deleted_length, inserted_text }`
(`crates/core/src/source.rs:20-24`) - byte offsets, not lines. Application then does the thing that
matters: mutate, then **incrementally reparse against the old tree** - `perform_edit(...); self.tree =
self.parse(Some(&self.tree))?` (`crates/core/src/tree_sitter/mod.rs:96-101`). The CST stays valid across
a batch, so edit N+1 anchors in post-edit-N reality, not stale coordinates.

**What the field ships instead.**

- grok-build hashline: anchors are `LINE:HASH` (or `LINE:HASH:HASH`) - a whitespace-normalized per-line
  hash plus a chunk fingerprint over 8 lines, hash_len 3
  (`crates/codegen/xai-grok-tools/src/implementations/grok_build_hashline/config.rs:15-40`, `.../scheme.rs:1-18`).
  Batches validate against the pre-edit snapshot and apply bottom-up, all-or-nothing (`.../edit/mod.rs:1-6`, `:50-53`).
- opencode edit: nine text `Replacer` generators tried in order - `SimpleReplacer`, `LineTrimmedReplacer`,
  `BlockAnchorReplacer`, `WhitespaceNormalizedReplacer`, `IndentationFlexibleReplacer`,
  `EscapeNormalizedReplacer`, `TrimmedBoundaryReplacer`, `ContextAwareReplacer`, `MultiOccurrenceReplacer`
  (`packages/opencode/src/tool/edit.ts:694-704`). `BlockAnchorReplacer` matches trimmed first and last
  line with `Math.max(1, floor(blockSize * 0.25))` line tolerance plus a middle-line similarity score
  (`edit.ts:288-340`).

**Where they fail and node anchoring does not.** Both are fuzzy string schemes with no notion of scope.
hashline's fingerprint is explicitly a freshness tradeoff - `ContentOnly` has "weakest freshness - edits
above a line do not invalidate its anchor", chunk mode invalidates "only anchors within the affected
chunk" (`scheme.rs:5-11`); anything outside the chunk stays silently "valid" while meaning something
else. opencode's ladder is worse: a block whose first and last lines are `  }` and `}` matches many
places, and the tolerance is *proportional to block size*, so bigger edits get looser. Neither can
express "the body of method `foo` on class `Bar`". Node anchoring can, and it cannot land inside a
string literal, a comment, or the wrong one of two identical `if err != nil {` blocks - the anchor is a
path through the tree, not a shape of text.

**Copy this.** `Edit{position, deleted_length, inserted_text}` + reparse-with-old-tree, and bottom-up
all-or-nothing batches (hashline gets that part right). **Skip** the nine fallback replacers - each is a
place a correct-looking edit lands in the wrong scope, and the ordering is unexplainable to someone
reviewing on a phone.

## 4. Cheap syntax gates - `has_error` before diagnostics

**What.** aider parses every edited file and reports the line of every `ERROR` or `is_missing` node -
`if node.type == "ERROR" or node.is_missing`, recursive (`aider/linter.py:260-269`), wrapped as
`basic_lint` (`linter.py:201-231`). It is the *default* when a language has no configured lint command
(`linter.py:106`); for Python it runs alongside `compile()` and a fatal-only flake8 subset
`E9,F821,F823,F831,F406,F407,F701,F702,F704,F706` (`linter.py:118-137`).

**The loop closes in-turn.** After edits: lint, and on failure set `reflected_message`, which becomes the
next user message (`base_coder.py:1599-1607`, `:944`), bounded at `max_reflections = 3`
(`base_coder.py:100-101`, warning at `:939`). Payload is `"# Fix any errors below, if possible.\n\n"`
plus errors plus TreeContext source with bad lines marked (`linter.py:111-116`, `:234-256`).

**The negative finding.** Nobody else does this. grok-build calls `has_error()` in exactly one place -
shell permission parsing (`crates/codegen/xai-grok-workspace/src/permission/shell_access.rs:52`) - never
on an edit. opencode has no tree-sitter on the edit path at all; its only agent-side use is shell parsing
(`packages/opencode/src/tool/shell.ts:9`), and `packages/core/src/tool/bash.ts:66` is still `TODO: Port
tree-sitter bash / PowerShell parser-based approval reduction`. The cheapest correctness check in the
field is implemented once.

**Copy this.** All of it. Parse-after-edit is sub-millisecond, needs no LSP, no project config, no
language-server startup, and catches the most common model failure - an unbalanced brace from a
mis-anchored replace - before the LSP has indexed. Bound retries. **Skip** the `if lang == "typescript":
return` bailout (`linter.py:210-212`, a grammar-version workaround) and the interactive
`confirm_ask("Attempt to fix lint errors?")` (`base_coder.py:1604`) - a headless/remote harness decides
by policy, not prompt.

## 5. Symbol-graph context selection - grok-build's xai-codebase-graph

**What.** ~9.7k LOC Rust crate, "High-performance code graph generation using tree-sitter queries" -
go-to-definition/references, initial + incremental indexing, rayon, and "Memory-mapped I/O: Zero-copy
file reading and fast index caching" (`crates/codegen/xai-codebase-graph/src/lib.rs:1-11`).

**How.** Same `@name.definition.*` / `@name.reference.*` convention as aider, but queries are Rust string
literals in per-language configs (`src/languages/rust.rs:22-86`), and only five languages: rust, ts, js,
go, python (`src/languages/mod.rs:35-41`) against aider's 58. Indexing is two-phase and memory-bounded -
parse batches in parallel from mmap, merge each into one `ScopeGraphIndex` with a single
`StringInterner`, drop the batch (`src/manager/builder.rs:271-340`). Cache is a custom binary format,
magic bytes `SGIX` at `.goto_index.bin`, with legacy-format detection returning `LegacyFormat` so the
caller rebuilds instead of deserializing garbage (`src/manager/cache.rs:1-4`, `:48-77`). Files over 5 MiB
are skipped (`src/index_manager.rs:42`). fs-notify `FileEvent`s feed an `IndexManager` actor answering in
place - "Prefer these over `get_snapshot()` in hot paths" (`lib.rs:42-46`).

**The finding that matters.** It is **not exposed to the model.** It is surfaced as ACP extension methods
to the *client*: `x.ai/code/goto-definition`, `x.ai/code/goto-references`, `x.ai/code/find-definitions`,
`x.ai/code/find-references`, `x.ai/code/status` (`crates/codegen/xai-grok-shell/src/extensions/code_nav.rs:8-14`),
gated on a client capability parsed at `initialize` (`agent/mvp_agent/acp_agent.rs:141-146`). No repo-map
block, no ranking, no token budget. grok built the better index and gave it to the IDE; aider built a
worse one and gave it to the LLM. Aider's is the one that changes generated code.

**Copy this.** The engineering - mmap + rayon + interning + batched merge, the `SGIX` magic-byte cache
with explicit legacy rejection, fs-notify -> actor -> in-place query, the 5 MiB skip. That is what makes
a symbol index affordable on *every* turn, not once per session. **Skip** queries as inline Rust literals
(recompile to add a language) and the five-language ceiling - use aider's `.scm` files, the capture
convention is already identical so the two are wire-compatible.

## 6. Footnote - tree-sitter for permissions

opencode parses the proposed command with tree-sitter-bash and tree-sitter-powershell WASM, lazily loaded
(`packages/opencode/src/tool/shell.ts:9`, `:312-336`), walks command nodes to pull real argument paths for
a filesystem-touching command set (`shell.ts:27-60`), and resolves each to an absolute path before
matching rules (`shell.ts:358-378`). grok-build does the same on the deny side and adds the failure
semantics: an unparseable command returns `AskFailClosed`, `root.has_error()` raises an Ask floor for
recursively entered scripts, ambiguous redirect targets force Ask, and since cwd is not tracked across
`cd`/`pushd`/`env -C` any relative operand after one is "unpinnable -> Ask"
(`crates/codegen/xai-grok-workspace/src/permission/shell_access.rs:40-78`). The lesson is the fail-closed
direction, not the parsing: structure tells you when you *cannot* decide.

## The one recommendation

**Make the tree-sitter CST the anchor for edits and the gate on their result, in the same tool call - and
build the repo map off the same tag stream.**

One `.scm` per language with aider's `@name.definition.*` / `@name.reference.*` contract feeds three
consumers from one parse: the ranked repo map (aider's PageRank + personalization + measured
binary-search budget, `repomap.py:487-706`); a node-anchored edit whose span is a byte range from the
matched node, reparsed incrementally against the old tree (`ast-grep crates/core/src/tree_sitter/mod.rs:96-101`,
`fixer.rs:181-190`); and a `has_error`/`is_missing` scan on the result that fails the tool call *before it
returns*, handing the error lines back as tool output (`aider/linter.py:260-269`,
`base_coder.py:1599-1607`, bounded at 3).

The field has all three parts and nobody has them wired together: aider has the map and the syntax gate
but text-based edits; ast-grep has node-anchored edits but no map and no post-edit gate; grok-build has
the best index and never shows it to the model, and reaches for `has_error` only to decide permissions.

For flowy specifically: the edit is already an event in the DAG, so record the *node path + byte range +
pre-edit tree hash*, not a line number. A line number is meaningless after HLC-merging a concurrent edit
from another node; a node path re-resolves against the merged tree. And the `has_error` bit becomes a
signed, replayable "this artifact parsed" fact a phone client renders as one green dot instead of a diff.
