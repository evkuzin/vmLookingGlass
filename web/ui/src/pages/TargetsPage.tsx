import {
  Alert,
  Badge,
  Box,
  Button,
  Card,
  Group,
  Loader,
  Pagination,
  Stack,
  Table,
  Text,
  TextInput,
  Tooltip,
} from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import {
  IconAlertTriangle,
  IconChevronDown,
  IconChevronRight,
  IconSearch,
} from "@tabler/icons-react";
import { useState, type FC } from "react";
import { StringParam, useQueryParam, withDefault } from "use-query-params";

import { nextSort, SortHeaders, type SortColumn, type SortState } from "../components/SortHeader";

import { useAPI } from "../api/api";
import type { AgentsResult, SearchResult, Target } from "../api/types";
import { HealthBadge } from "../components/HealthBadge";
import { LabelBadges } from "../components/LabelBadges";
import { formatCount, formatDuration, formatRelative } from "../lib/format";

const pageSize = 50;

// FleetStats is the landing view: the fleet at a glance, shown before any
// query narrows things down.
const FleetStats: FC = () => {
  const { data, isLoading } = useAPI<AgentsResult>([], "agents", undefined, {
    refetchInterval: 15000,
  });

  if (isLoading || !data) {
    return <Loader size="sm" />;
  }

  const healthy = data.agents.filter((a) => a.healthy).length;
  const stale = data.agents.filter((a) => a.stale).length;

  return (
    <Group gap="xl">
      <Stat label="Agents" value={`${healthy} / ${data.agents.length}`} warn={healthy < data.agents.length} />
      <Stat label="Active targets" value={formatCount(data.activeCount)} />
      <Stat label="Up" value={formatCount(data.upCount)} />
      <Stat label="Down" value={formatCount(data.downCount)} warn={data.downCount > 0} />
      <Stat label="Dropped" value={formatCount(data.droppedCount)} />
      {stale > 0 && <Stat label="Stale agents" value={String(stale)} warn />}
    </Group>
  );
};

const Stat: FC<{ label: string; value: string; warn?: boolean }> = ({ label, value, warn }) => (
  <Stack gap={0}>
    <Text size="xs" c="dimmed">
      {label}
    </Text>
    <Text size="xl" fw={600} c={warn ? "red" : undefined}>
      {value}
    </Text>
  </Stack>
);

// TargetRow renders one merged target. Discovered labels stay collapsed:
// they are large and only wanted when debugging relabel rules.
const TargetRow: FC<{ target: Target }> = ({ target }) => {
  const [expanded, setExpanded] = useState(false);
  const instance = target.labels.instance || target.discoveredLabels.__address__ || "";

  return (
    <>
      <Table.Tr style={{ cursor: "pointer" }} onClick={() => setExpanded((e) => !e)}>
        <Table.Td>
          {expanded ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
        </Table.Td>
        <Table.Td>
          <Text size="sm" fw={500}>
            {instance}
          </Text>
        </Table.Td>
        <Table.Td>
          <Text size="sm">{target.scrapePool}</Text>
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap">
            <HealthBadge health={target.health} />
            {target.disputed && (
              <Tooltip
                withArrow
                label={Object.entries(target.healthByAgent ?? {})
                  .map(([a, h]) => `${a}: ${h}`)
                  .join(", ")}
              >
                <Badge color="orange" variant="filled" leftSection={<IconAlertTriangle size={11} />}>
                  disputed
                </Badge>
              </Tooltip>
            )}
          </Group>
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="wrap">
            {target.agents.map((a) => (
              <Badge key={a} variant="outline" color={target.multiScraped ? "orange" : "gray"}>
                {a}
              </Badge>
            ))}
          </Group>
        </Table.Td>
        <Table.Td>
          <Text size="sm" c="dimmed">
            {target.lastScrape ? formatRelative(target.lastScrape) : "-"}
          </Text>
        </Table.Td>
        <Table.Td>
          <Text size="sm" c="dimmed">
            {formatDuration(target.lastScrapeDuration)}
          </Text>
        </Table.Td>
        <Table.Td>
          <Text size="sm" c="dimmed">
            {formatCount(target.lastSamplesScraped)}
          </Text>
        </Table.Td>
      </Table.Tr>

      {expanded && (
        <Table.Tr>
          <Table.Td colSpan={8}>
            <Stack gap="sm" p="sm">
              {target.disputed && (
                <Alert color="orange" icon={<IconAlertTriangle size={14} />} title="Agents disagree">
                  This target is reachable from some agents and not others, which
                  points at a network fault rather than the target being down.
                  <Stack gap={2} mt="xs">
                    {Object.entries(target.healthByAgent ?? {}).map(([a, h]) => (
                      <Text key={a} size="xs">
                        {a}: <b>{h}</b>
                      </Text>
                    ))}
                  </Stack>
                </Alert>
              )}
              {target.lastError && (
                <Alert color="red" icon={<IconAlertTriangle size={14} />} title="Last scrape error">
                  {target.lastError}
                </Alert>
              )}
              <Text size="xs" c="dimmed">
                {target.scrapeUrl}
              </Text>
              <Box>
                <Text size="xs" fw={600} mb={4}>
                  Labels
                </Text>
                <LabelBadges labels={target.labels} />
              </Box>
              <Box>
                <Text size="xs" fw={600} mb={4}>
                  Discovered labels (pre-relabeling)
                </Text>
                <LabelBadges labels={target.discoveredLabels} variant="discovered" />
              </Box>
            </Stack>
          </Table.Td>
        </Table.Tr>
      )}
    </>
  );
};

// Sorting is server-side: results are paged, so ordering only the returned
// page would rank it against itself rather than against every match. The
// value functions are unused here for that reason; the key is sent to the API.
const columns: SortColumn<Target>[] = [
  { key: "instance", label: "Instance", value: () => 0 },
  { key: "pool", label: "Pool", value: () => 0 },
  { key: "health", label: "Health", value: () => 0 },
  { key: "agents", label: "Agents", value: () => 0 },
  { key: "lastScrape", label: "Last scrape", value: () => 0, descFirst: true },
  { key: "duration", label: "Duration", value: () => 0, descFirst: true },
  { key: "samples", label: "Samples", value: () => 0, descFirst: true },
];

const examples = [
  "health=down",
  "labels.job=node",
  "agent=vmagent01",
  "state=dropped",
  "lastScrapeDuration>1",
];

export const TargetsPage: FC = () => {
  const [filter, setFilter] = useQueryParam("filter", withDefault(StringParam, ""));
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<SortState>({ by: "", desc: false });
  const [debounced] = useDebouncedValue(filter.trim(), 300);

  const params = new URLSearchParams();
  if (debounced) {
    // Filters are space-separated and ANDed, matching the CLI.
    for (const f of debounced.split(/\s+/)) {
      params.append("filter", f);
    }
  }
  params.set("limit", String(pageSize));
  params.set("offset", String((page - 1) * pageSize));
  if (sort.by) {
    params.set("sort", sort.by);
    params.set("order", sort.desc ? "desc" : "asc");
  }

  // The fleet holds far more targets than any page can show, so nothing is
  // fetched until a filter narrows it.
  const enabled = debounced !== "";
  const { data, isLoading, error } = useAPI<SearchResult>(
    [debounced, page, sort.by, sort.desc],
    "search",
    params,
    { enabled },
  );

  const apply = (f: string) => {
    setFilter(f);
    setPage(1);
  };

  return (
    <Stack gap="lg">
      <Card withBorder padding="md">
        <FleetStats />
      </Card>

      <TextInput
        size="md"
        placeholder="Filter targets, e.g. health=down labels.job=node"
        leftSection={<IconSearch size={16} />}
        value={filter}
        onChange={(e) => apply(e.currentTarget.value)}
      />

      <Group gap="xs">
        <Text size="xs" c="dimmed">
          Examples:
        </Text>
        {examples.map((e) => (
          <Button key={e} size="compact-xs" variant="subtle" onClick={() => apply(e)}>
            {e}
          </Button>
        ))}
      </Group>

      {!enabled && (
        <Text c="dimmed" ta="center" py="xl">
          Enter a filter to search targets across every agent.
        </Text>
      )}

      {error && (
        <Alert color="red" icon={<IconAlertTriangle size={16} />} title="Search failed">
          {error.message}
        </Alert>
      )}

      {enabled && isLoading && <Loader />}

      {data && (
        <>
          <Group justify="space-between">
            <Text size="sm" c="dimmed">
              {formatCount(data.total)} matching {data.total === 1 ? "target" : "targets"}
            </Text>
            {data.total > pageSize && (
              <Pagination
                size="sm"
                value={page}
                onChange={setPage}
                total={Math.ceil(data.total / pageSize)}
              />
            )}
          </Group>

          <Table highlightOnHover striped>
            <Table.Thead>
              <SortHeaders
                columns={columns}
                sort={sort}
                onSort={(c) => {
                  setSort(nextSort(sort, c));
                  // Sorting reorders the whole match set, so the old page
                  // number no longer refers to the same rows.
                  setPage(1);
                }}
                leading={() => <Table.Th w={30} />}
              />
            </Table.Thead>
            <Table.Tbody>
              {data.targets.map((t) => (
                <TargetRow key={`${t.scrapeUrl}|${JSON.stringify(t.labels)}`} target={t} />
              ))}
            </Table.Tbody>
          </Table>

          {data.targets.length === 0 && (
            <Text c="dimmed" ta="center" py="xl">
              No targets matched.
            </Text>
          )}
        </>
      )}
    </Stack>
  );
};
