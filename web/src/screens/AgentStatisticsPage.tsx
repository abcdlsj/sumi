import { useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { Activity, ArrowDownToLine, ArrowUpFromLine, BarChart3, Gauge } from "lucide-react";

import {getComputerLlmUsage, listAgents, listComputers} from "../api/client";
import type {LlmUsageAgentSeries, LlmUsageBucket} from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { PixelIdentity } from "../components/PixelIdentity";
import { useSpaceShell } from "../components/spaceShellContext";
import { BreakdownTable, StatCard, TokenUsageChart, formatCount } from "../components/TokenUsageChart";
import { UsageRangeSwitch, type LlmUsageRange } from "../components/LlmUsagePanel";
import "./agentStatistics.css";
import { PanelSkeleton } from "../components/Skeleton";


interface MergedAgent {
  member_id: string;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
  series: LlmUsageBucket[];
  models: MergedModel[];
}

interface MergedModel {
  key: string;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
}

export function AgentStatisticsPage() {
  return <AgentStatisticsWorkspace spaceId={useSpaceShell().space.id} />;
}

export function AgentStatisticsWorkspace({ spaceId }: { spaceId: string }) {
  const [range, setRange] = useState<LlmUsageRange>("24h");
  const [selectedMemberId, setSelectedMemberId] = useState<string>();
  // The Shell already holds the Space and its members; only these two lists are page-specific.
  const agents = useQuery({
    // Same keys as every other Agents and Computers reader, so `agent.changed`,
    // `computer.changed` and their member events refresh this page too.
    queryKey: ["agents", spaceId],
    queryFn: () => listAgents(spaceId),
  });
  const computers = useQuery({
    queryKey: ["computers", spaceId],
    queryFn: () => listComputers(spaceId),
  });
  const usageResults = useQueries({
    queries: (computers.data ?? []).map((computer) => ({
      queryKey: ["llm-usage", computer.id, range],
      queryFn: () => getComputerLlmUsage(computer.id, range),
      enabled: computer.status === "online",
      retry: false,
    })),
  });

  const merged = useMemo(() => {
    const byAgent = new Map<string, MergedAgent>();
    const byAgentModels = new Map<string, Map<string, MergedModel>>();
    for (const result of usageResults) {
      const data = result.data;
      if (!data) continue;
      for (const entry of data.by_agent_series) {
        byAgent.set(entry.agent_id, mergeAgent(byAgent.get(entry.agent_id), entry));
      }
      for (const row of data.by_agent_model) {
        const models = byAgentModels.get(row.agent_id) ?? new Map<string, MergedModel>();
        const previous = models.get(row.model);
        models.set(row.model, {
          key: row.model,
          requests: (previous?.requests ?? 0) + row.requests,
          input_tokens: (previous?.input_tokens ?? 0) + row.input_tokens,
          output_tokens: (previous?.output_tokens ?? 0) + row.output_tokens,
          cached_input_tokens: (previous?.cached_input_tokens ?? 0) + row.cached_input_tokens,
        });
        byAgentModels.set(row.agent_id, models);
      }
    }
    for (const [agentId, agent] of byAgent) {
      agent.models = [...(byAgentModels.get(agentId)?.values() ?? [])].sort(
        (a, b) => b.input_tokens - a.input_tokens,
      );
    }
    return {
      agents: [...byAgent.values()].sort((a, b) => b.input_tokens - a.input_tokens),
    };
  }, [usageResults]);

  const onlineComputerCount = (computers.data ?? []).filter(
    (computer) => computer.status === "online",
  ).length;
  const usageErrorResults = usageResults.filter((result, index) => {
    const computer = computers.data?.[index];
    return computer?.status === "online" && result.isError;
  });
  const usageDataResults = usageResults.filter((result) => Boolean(result.data));
  const usagePending = usageResults.some((result, index) => {
    const computer = computers.data?.[index];
    return computer?.status === "online" && (result.isPending || result.isLoading);
  });
  const usageUnavailable =
    !usagePending &&
    onlineComputerCount > 0 &&
    usageDataResults.length === 0 &&
    usageErrorResults.length >= onlineComputerCount;
  const usagePartial = usageErrorResults.length > 0 && usageDataResults.length > 0;
  const retryUsage = () => {
    for (const result of usageErrorResults) {
      void result.refetch();
    }
  };
  const sourceUnavailable = agents.isError || computers.isError;
  const sourcePending = agents.isPending || computers.isPending;
  const retrySources = () => {
    if (agents.isError) void agents.refetch();
    if (computers.isError) void computers.refetch();
  };

  const agentName = (memberId: string) =>
    agents.data?.find((agent) => agent.member_id === memberId)?.name ?? memberId.slice(0, 8);
  const selectedAgent =
    merged.agents.find((agent) => agent.member_id === selectedMemberId) ??
    merged.agents[0] ??
    null;

  return (
    <div className="agent-statistics">
      <header className="agent-statistics-header">
        <div>
          <h1>Agent statistics</h1>
        </div>
        <UsageRangeSwitch range={range} onChange={setRange} />
      </header>

      {sourcePending ? (
        <PanelSkeleton label="Loading Agent statistics" />
      ) : sourceUnavailable ? (
        <div className="agent-statistics-empty" role="status">
          <p>Unable to load Agent statistics sources.</p>
          <button type="button" onClick={retrySources}>
            Retry
          </button>
        </div>
      ) : usagePending && usageDataResults.length === 0 ? (
        <PanelSkeleton label="Loading LLM usage" />
      ) : usageUnavailable ? (
        <div className="agent-statistics-empty" role="status">
          <p>Unable to load LLM usage from the selected Computers.</p>
          <button type="button" onClick={retryUsage}>
            Retry
          </button>
        </div>
      ) : merged.agents.length === 0 || !selectedAgent ? (
        <EmptyState icon={BarChart3} title={usagePartial ? "Usage is incomplete" : "No usage yet"}>
          {usagePartial
            ? "No complete LLM usage data is available from the responding Computers."
            : "No LLM usage recorded yet. Usage appears after an Agent runs a model turn."}
        </EmptyState>
      ) : (
        <>
          {usagePartial ? (
            <p className="agent-statistics-partial" role="status">
              Some Computers could not be queried. Showing available usage data.
            </p>
          ) : null}
          <div className="agent-statistics-layout">
            <ul className="agent-statistics-list" aria-label="Agents with usage">
              {merged.agents.map((agent) => (
                <li key={agent.member_id}>
                  <button
                    type="button"
                    className={agent.member_id === selectedAgent.member_id ? "agent-statistics-item--active" : undefined}
                    onClick={() => setSelectedMemberId(agent.member_id)}
                  >
                    <PixelIdentity name={agentName(agent.member_id)} kind="agent" seed={agent.member_id} />
                    <span>
                      <strong>{agentName(agent.member_id)}</strong>
                      <small>{formatCount(agent.input_tokens)} input · {formatCount(agent.output_tokens)} output</small>
                    </span>
                  </button>
                </li>
              ))}
            </ul>

            <section className="agent-statistics-detail" aria-label={`${agentName(selectedAgent.member_id)} usage`}>
              <div className="llm-usage-cards" aria-label="Agent usage summary">
                <StatCard icon={Activity} label="Requests" value={formatCount(selectedAgent.requests)} />
                <StatCard icon={ArrowUpFromLine} label="Input tokens" value={formatCount(selectedAgent.input_tokens)} />
                <StatCard icon={ArrowDownToLine} label="Output tokens" value={formatCount(selectedAgent.output_tokens)} />
                <StatCard
                  icon={Gauge}
                  label="Cached input"
                  value={formatCount(selectedAgent.cached_input_tokens)}
                  detail={`${Math.round((selectedAgent.cached_input_tokens / Math.max(1, selectedAgent.input_tokens)) * 100)}% of input`}
                />
              </div>
              <TokenUsageChart series={selectedAgent.series} />
              <BreakdownTable title="By model" rows={selectedAgent.models} />
            </section>
          </div>
        </>
      )}
    </div>
  );
}

function mergeAgent(
  previous: MergedAgent | undefined,
  entry: LlmUsageAgentSeries,
): MergedAgent {
  const series = new Map(previous?.series.map((bucket) => [bucket.bucket, bucket]));
  for (const bucket of entry.series) {
    const existing = series.get(bucket.bucket);
    series.set(bucket.bucket, {
      bucket: bucket.bucket,
      requests: (existing?.requests ?? 0) + bucket.requests,
      input_tokens: (existing?.input_tokens ?? 0) + bucket.input_tokens,
      output_tokens: (existing?.output_tokens ?? 0) + bucket.output_tokens,
      cached_input_tokens: (existing?.cached_input_tokens ?? 0) + bucket.cached_input_tokens,
    });
  }
  return {
    member_id: entry.agent_id,
    requests: (previous?.requests ?? 0) + entry.requests,
    input_tokens: (previous?.input_tokens ?? 0) + entry.input_tokens,
    output_tokens: (previous?.output_tokens ?? 0) + entry.output_tokens,
    cached_input_tokens: (previous?.cached_input_tokens ?? 0) + entry.cached_input_tokens,
    series: [...series.values()].sort((a, b) => a.bucket.localeCompare(b.bucket)),
    models: previous?.models ?? [],
  };
}
