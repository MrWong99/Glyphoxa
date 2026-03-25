"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import type { KnowledgeEntity, KnowledgeRelationship } from "@/lib/types";

// Color palette for entity types.
const TYPE_COLORS: Record<string, string> = {
  npc: "#60a5fa",     // blue
  player: "#34d399",  // green
  location: "#fbbf24", // yellow
  item: "#a78bfa",    // purple
  faction: "#f87171", // red
  event: "#fb923c",   // orange
  quest: "#2dd4bf",   // teal
  concept: "#e879f9", // pink
};

function getColor(type: string): string {
  return TYPE_COLORS[type] ?? "#94a3b8"; // gray fallback
}

interface Node {
  id: string;
  name: string;
  type: string;
  x: number;
  y: number;
  vx: number;
  vy: number;
}

interface Props {
  entities: KnowledgeEntity[];
  relationships: KnowledgeRelationship[];
}

const W = 800;
const H = 500;

function initNodes(ents: KnowledgeEntity[]): Node[] {
  return ents.map((e, i) => {
    const angle = (2 * Math.PI * i) / ents.length;
    const radius = Math.min(W, H) * 0.3;
    return {
      id: e.id,
      name: e.name,
      type: e.type,
      x: W / 2 + radius * Math.cos(angle) + (Math.random() - 0.5) * 40,
      y: H / 2 + radius * Math.sin(angle) + (Math.random() - 0.5) * 40,
      vx: 0,
      vy: 0,
    };
  });
}

export function KnowledgeGraphVisualization({ entities, relationships }: Props) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [hoveredNode, setHoveredNode] = useState<string | null>(null);
  const [draggedNode, setDraggedNode] = useState<string | null>(null);

  // Derive edges from relationships (no mutation needed).
  const edges = useMemo(
    () => relationships.map((r) => ({ source: r.source_id, target: r.target_id, label: r.rel_type })),
    [relationships],
  );

  // Reset nodes when entities change.
  const [nodes, setNodes] = useState<Node[]>(() => initNodes(entities));
  const [prevEntities, setPrevEntities] = useState(entities);
  if (entities !== prevEntities) {
    setPrevEntities(entities);
    setNodes(initNodes(entities));
  }

  // Force simulation via requestAnimationFrame.
  useEffect(() => {
    let frameId: number;

    function tick() {
      setNodes((prev) => {
        if (prev.length === 0) return prev;
        const ns = prev.map((n) => ({ ...n }));

        const alpha = 0.1;
        const repulsion = 2000;
        const attraction = 0.005;
        const centerForce = 0.01;

        // Repulsion between all node pairs.
        for (let i = 0; i < ns.length; i++) {
          for (let j = i + 1; j < ns.length; j++) {
            const dx = ns[i].x - ns[j].x;
            const dy = ns[i].y - ns[j].y;
            const dist = Math.sqrt(dx * dx + dy * dy) || 1;
            const force = repulsion / (dist * dist);
            const fx = (dx / dist) * force * alpha;
            const fy = (dy / dist) * force * alpha;
            ns[i].vx += fx;
            ns[i].vy += fy;
            ns[j].vx -= fx;
            ns[j].vy -= fy;
          }
        }

        // Attraction along edges.
        const nodeMap = new Map(ns.map((n) => [n.id, n]));
        for (const edge of edges) {
          const s = nodeMap.get(edge.source);
          const t = nodeMap.get(edge.target);
          if (!s || !t) continue;
          const dx = t.x - s.x;
          const dy = t.y - s.y;
          const dist = Math.sqrt(dx * dx + dy * dy) || 1;
          const force = dist * attraction * alpha;
          s.vx += (dx / dist) * force;
          s.vy += (dy / dist) * force;
          t.vx -= (dx / dist) * force;
          t.vy -= (dy / dist) * force;
        }

        // Center gravity + velocity decay.
        for (const n of ns) {
          if (n.id === draggedNode) continue;
          n.vx += (W / 2 - n.x) * centerForce * alpha;
          n.vy += (H / 2 - n.y) * centerForce * alpha;
          n.vx *= 0.85;
          n.vy *= 0.85;
          n.x += n.vx;
          n.y += n.vy;
          // Clamp to bounds.
          n.x = Math.max(30, Math.min(W - 30, n.x));
          n.y = Math.max(30, Math.min(H - 30, n.y));
        }

        return ns;
      });
      frameId = requestAnimationFrame(tick);
    }

    frameId = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frameId);
  }, [edges, draggedNode]);

  const nodeMap = new Map(nodes.map((n) => [n.id, n]));

  function handleMouseDown(nodeId: string) {
    setDraggedNode(nodeId);
  }

  function handleMouseMove(e: React.MouseEvent<SVGSVGElement>) {
    if (!draggedNode || !svgRef.current) return;
    const rect = svgRef.current.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    setNodes((prev) =>
      prev.map((n) => (n.id === draggedNode ? { ...n, x, y, vx: 0, vy: 0 } : n)),
    );
  }

  function handleMouseUp() {
    setDraggedNode(null);
  }

  return (
    <svg
      ref={svgRef}
      viewBox={`0 0 ${W} ${H}`}
      className="w-full rounded-lg border border-border/50 bg-background"
      style={{ minHeight: 400 }}
      onMouseMove={handleMouseMove}
      onMouseUp={handleMouseUp}
      onMouseLeave={handleMouseUp}
    >
      <defs>
        <marker
          id="arrowhead"
          markerWidth="6"
          markerHeight="4"
          refX="6"
          refY="2"
          orient="auto"
        >
          <polygon points="0 0, 6 2, 0 4" fill="currentColor" className="text-muted-foreground/40" />
        </marker>
      </defs>

      {/* Edges */}
      {edges.map((edge, i) => {
        const s = nodeMap.get(edge.source);
        const t = nodeMap.get(edge.target);
        if (!s || !t) return null;
        const isHighlighted =
          hoveredNode === edge.source || hoveredNode === edge.target;

        return (
          <g key={i}>
            <line
              x1={s.x}
              y1={s.y}
              x2={t.x}
              y2={t.y}
              stroke={isHighlighted ? "currentColor" : "currentColor"}
              className={
                isHighlighted
                  ? "text-primary/60"
                  : "text-muted-foreground/20"
              }
              strokeWidth={isHighlighted ? 2 : 1}
              markerEnd="url(#arrowhead)"
            />
            {isHighlighted && (
              <text
                x={(s.x + t.x) / 2}
                y={(s.y + t.y) / 2 - 6}
                fill="currentColor"
                className="text-muted-foreground"
                fontSize="10"
                textAnchor="middle"
              >
                {edge.label}
              </text>
            )}
          </g>
        );
      })}

      {/* Nodes */}
      {nodes.map((node) => {
        const isHovered = hoveredNode === node.id;
        const color = getColor(node.type);
        const r = isHovered ? 10 : 7;

        return (
          <g
            key={node.id}
            onMouseEnter={() => setHoveredNode(node.id)}
            onMouseLeave={() => setHoveredNode(null)}
            onMouseDown={() => handleMouseDown(node.id)}
            style={{ cursor: draggedNode === node.id ? "grabbing" : "grab" }}
          >
            <circle
              cx={node.x}
              cy={node.y}
              r={r + 2}
              fill={color}
              opacity={0.15}
            />
            <circle cx={node.x} cy={node.y} r={r} fill={color} />
            <text
              x={node.x}
              y={node.y + r + 14}
              fill="currentColor"
              className="text-foreground"
              fontSize="11"
              fontWeight={isHovered ? "bold" : "normal"}
              textAnchor="middle"
            >
              {node.name.length > 18
                ? node.name.slice(0, 16) + "..."
                : node.name}
            </text>
            {isHovered && (
              <text
                x={node.x}
                y={node.y - r - 6}
                fill={color}
                fontSize="9"
                textAnchor="middle"
              >
                {node.type}
              </text>
            )}
          </g>
        );
      })}
    </svg>
  );
}
