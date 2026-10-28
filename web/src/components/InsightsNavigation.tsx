import { Link, useLocation } from "@tanstack/react-router";
import { BarChart3, Network } from "lucide-react";

export function InsightsTabs({ spaceSlug }: { spaceSlug: string }) {
  const path = useLocation().pathname;
  return (
    <nav className="page-tabs" aria-label="Agent insights">
      <Link
        to="/s/$spaceSlug/insights/stats"
        params={{ spaceSlug }}
        aria-current={path.endsWith("/insights/stats") ? "page" : undefined}
      >
        <BarChart3 aria-hidden="true" />
        Statistics
      </Link>
      <Link
        to="/s/$spaceSlug/insights/graph"
        params={{ spaceSlug }}
        aria-current={path.endsWith("/insights/graph") ? "page" : undefined}
      >
        <Network aria-hidden="true" />
        Graph
      </Link>
    </nav>
  );
}
