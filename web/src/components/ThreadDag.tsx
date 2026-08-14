import {
  Background,
  BackgroundVariant,
  Controls,
  type Edge,
  MarkerType,
  type Node,
  ReactFlow,
} from "@xyflow/react";
import { useMemo } from "react";

import { type FlowyEvent, isAgent } from "@/lib/api";
import { shortId } from "@/lib/utils";

const ROW = 92;
const LANE = 210;

/**
 * The thread, drawn from parents.
 *
 * It reads like a branch graph in git because it is one: a message with one
 * parent continues the line, two messages naming the same parent fork it into
 * two lanes, and a message naming two parents pulls them back together. The
 * lane assignment is the same rule a git log graph uses - the first child stays
 * on its parent's lane and every later one starts a new lane to the right.
 */
export function threadLayout(events: FlowyEvent[]): { nodes: Node[]; edges: Edge[] } {
  const ordered = [...events].sort((a, b) => a.seq_hlc - b.seq_hlc || a.id.localeCompare(b.id));
  const known = new Set(ordered.map((event) => event.id));
  const lanes = new Map<string, number>();
  const children = new Map<string, number>();
  let nextLane = 0;

  const nodes: Node[] = ordered.map((event, row) => {
    const parent = event.parents.find((id) => lanes.has(id));
    let lane: number;
    if (parent === undefined) {
      lane = nextLane++;
    } else {
      const taken = children.get(parent) ?? 0;
      children.set(parent, taken + 1);
      lane = taken === 0 ? (lanes.get(parent) ?? 0) : nextLane++;
    }
    lanes.set(event.id, lane);

    const agent = isAgent(event);
    return {
      id: event.id,
      position: { x: lane * LANE, y: row * ROW },
      draggable: false,
      data: {
        label: (
          <div className="w-44 text-left">
            <div className="pb-1 font-mono text-[10px] uppercase tracking-wide opacity-70">
              {agent ? "agent" : "human"} · #{shortId(event.id)}
            </div>
            <div className="line-clamp-3 text-xs leading-snug">{event.body}</div>
          </div>
        ),
      },
      style: {
        background: "var(--color-card)",
        color: "var(--color-foreground)",
        border: `1px solid ${agent ? "var(--color-agent)" : "var(--color-human)"}`,
        borderRadius: "var(--radius-md)",
        padding: "8px 10px",
        width: 200,
      },
    };
  });

  const edges: Edge[] = [];
  for (const event of ordered) {
    for (const parent of event.parents) {
      if (!known.has(parent)) continue;
      edges.push({
        id: `${parent}->${event.id}`,
        source: parent,
        target: event.id,
        type: "smoothstep",
        markerEnd: { type: MarkerType.ArrowClosed },
      });
    }
  }
  return { nodes, edges };
}

export function ThreadDag({ events }: { events: FlowyEvent[] }) {
  const { nodes, edges } = useMemo(() => threadLayout(events), [events]);

  if (events.length === 0) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-center text-muted-foreground text-sm">
        pick a message to see its thread
      </div>
    );
  }

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      fitView
      nodesConnectable={false}
      nodesDraggable={false}
      proOptions={{ hideAttribution: true }}
      className="h-full w-full"
    >
      <Background variant={BackgroundVariant.Dots} gap={18} size={1} color="var(--color-border)" />
      <Controls showInteractive={false} />
    </ReactFlow>
  );
}
