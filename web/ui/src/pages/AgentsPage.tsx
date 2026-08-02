import { Alert, Badge, Card, Group, Loader, Stack, Table, Text, Tooltip } from "@mantine/core";
import { IconAlertTriangle } from "@tabler/icons-react";
import { useMemo, useState, type FC } from "react";

import { useAPI } from "../api/api";
import type { Agent, AgentsResult } from "../api/types";
import {
  nextSort,
  SortHeaders,
  sortRows,
  type SortColumn,
  type SortState,
} from "../components/SortHeader";
import { UptimeBar } from "../components/UptimeBar";
import { formatCount, formatRelative } from "../lib/format";

const stateBadge = (healthy: boolean, stale: boolean) => {
  if (!healthy) {
    return <Badge color="red" variant="light">failing</Badge>;
  }
  if (stale) {
    return <Badge color="yellow" variant="light">stale</Badge>;
  }
  return <Badge color="green" variant="light">ok</Badge>;
};

// Sorting rank for the state column: the agents worth looking at first.
const stateRank = (a: Agent) => (!a.healthy ? 0 : a.stale ? 1 : 2);

// availability is the success rate over the retained history, used to sort
// the uptime column. Agents with no history sort last rather than as perfect.
const availability = (a: Agent) => {
  if (!a.history || a.history.length === 0) {
    return -1;
  }
  return a.history.filter((h) => h.ok).length / a.history.length;
};

const columns: SortColumn<Agent>[] = [
  { key: "name", label: "Agent", value: (a) => a.name },
  { key: "state", label: "State", value: stateRank },
  { key: "active", label: "Active", value: (a) => a.activeTargets, descFirst: true },
  { key: "up", label: "Up", value: (a) => a.upTargets, descFirst: true },
  { key: "down", label: "Down", value: (a) => a.downTargets, descFirst: true },
  { key: "dropped", label: "Dropped", value: (a) => a.droppedTargets, descFirst: true },
  { key: "lastPoll", label: "Last poll", value: (a) => a.lastPoll, descFirst: true },
  { key: "pollTime", label: "Poll time", value: (a) => a.durationMs, descFirst: true },
  { key: "error", label: "Error" },
  { key: "uptime", label: "Last 12h", value: availability },
];

export const AgentsPage: FC = () => {
  const { data, isLoading, error } = useAPI<AgentsResult>([], "agents", undefined, {
    refetchInterval: 15000,
  });

  // Failing agents first by default: that is what the page is for.
  const [sort, setSort] = useState<SortState>({ by: "state", desc: false });

  const sorted = useMemo(
    () => (data ? sortRows(data.agents, columns, sort, (a) => a.name) : []),
    [data, sort],
  );

  if (error) {
    return (
      <Alert color="red" icon={<IconAlertTriangle size={16} />} title="Could not load agents">
        {error.message}
      </Alert>
    );
  }
  if (isLoading || !data) {
    return <Loader />;
  }

  return (
    <Stack gap="lg">
      <Card withBorder padding="md">
        <Group gap="xl">
          <Text size="sm">
            <b>{data.agents.filter((a) => a.healthy).length}</b> of{" "}
            <b>{data.agents.length}</b> agents healthy
          </Text>
          <Text size="sm" c="dimmed">
            {formatCount(data.activeCount)} active, {formatCount(data.droppedCount)} dropped
          </Text>
        </Group>
      </Card>

      <Table highlightOnHover striped>
        <Table.Thead>
          <SortHeaders
            columns={columns}
            sort={sort}
            onSort={(c) => setSort(nextSort(sort, c))}
          />
        </Table.Thead>
        <Table.Tbody>
          {sorted.map((a) => (
            <Table.Tr key={a.name}>
              <Table.Td>
                <Tooltip label={a.url} withArrow>
                  <Text size="sm" fw={500}>{a.name}</Text>
                </Tooltip>
              </Table.Td>
              <Table.Td>{stateBadge(a.healthy, a.stale)}</Table.Td>
              <Table.Td>{formatCount(a.activeTargets)}</Table.Td>
              <Table.Td>{formatCount(a.upTargets)}</Table.Td>
              <Table.Td>
                <Text c={a.downTargets > 0 ? "red" : undefined}>
                  {formatCount(a.downTargets)}
                </Text>
              </Table.Td>
              <Table.Td>{formatCount(a.droppedTargets)}</Table.Td>
              <Table.Td>
                <Text size="sm" c="dimmed">{formatRelative(a.lastPoll)}</Text>
              </Table.Td>
              <Table.Td>
                <Text size="sm" c="dimmed">{a.durationMs}ms</Text>
              </Table.Td>
              <Table.Td>
                <Text size="xs" c="red">{a.error ?? ""}</Text>
              </Table.Td>
              <Table.Td>
                <UptimeBar history={a.history} />
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
};
