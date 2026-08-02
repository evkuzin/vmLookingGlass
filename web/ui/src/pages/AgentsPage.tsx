import { Alert, Badge, Card, Group, Loader, Stack, Table, Text, Tooltip } from "@mantine/core";
import { IconAlertTriangle } from "@tabler/icons-react";
import type { FC } from "react";

import { useAPI } from "../api/api";
import type { AgentsResult } from "../api/types";
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

export const AgentsPage: FC = () => {
  const { data, isLoading, error } = useAPI<AgentsResult>([], "agents", undefined, {
    refetchInterval: 15000,
  });

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
          <Table.Tr>
            <Table.Th>Agent</Table.Th>
            <Table.Th>State</Table.Th>
            <Table.Th>Active</Table.Th>
            <Table.Th>Up</Table.Th>
            <Table.Th>Down</Table.Th>
            <Table.Th>Dropped</Table.Th>
            <Table.Th>Last poll</Table.Th>
            <Table.Th>Poll time</Table.Th>
            <Table.Th>Error</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {data.agents.map((a) => (
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
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
};
