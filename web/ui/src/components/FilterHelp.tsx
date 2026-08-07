import { Anchor, Code, Popover, Stack, Table, Text } from "@mantine/core";
import { IconHelp } from "@tabler/icons-react";
import { useState, type FC } from "react";

type row = {
  filter: string;
  meaning: string;
};

const labelRows: row[] = [
  { filter: 'labels.instance=host042:9100', meaning: "label equals this exactly" },
  { filter: "labels.instance=~042", meaning: "label contains this anywhere" },
  { filter: "labels.job!=redis", meaning: "label is anything else, or is absent" },
  { filter: "labels.job!~redis", meaning: "label does not contain it, or is absent" },
  { filter: "labels.env=prod|staging", meaning: "either, since the value is a regex" },
];

const fieldRows: row[] = [
  { filter: "health=down", meaning: "up, down or unknown" },
  { filter: "state=dropped", meaning: "active, dropped or any" },
  { filter: "agent=vmagent01", meaning: "any agent scraping the target" },
  { filter: "scrapePool=consul.*", meaning: "matches the whole name, so anchor with .*" },
  { filter: "lastScrapeDuration>1", meaning: "seconds; also < >= <= == !=" },
  { filter: "lastSamplesScraped<100", meaning: "sample count" },
];

const Rows: FC<{ rows: row[] }> = ({ rows }) => (
  <Table withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}>
    <Table.Tbody>
      {rows.map((r) => (
        <Table.Tr key={r.filter}>
          <Table.Td style={{ whiteSpace: "nowrap", paddingRight: 16 }}>
            <Code>{r.filter}</Code>
          </Table.Td>
          <Table.Td>
            <Text size="xs" c="dimmed">
              {r.meaning}
            </Text>
          </Table.Td>
        </Table.Tr>
      ))}
    </Table.Tbody>
  </Table>
);

// FilterHelp explains the syntax next to the input. The rules that are not
// guessable are stated first: filters combine with a space, label lookups need
// the labels. prefix, and values are bare regexes rather than quoted strings.
export const FilterHelp: FC = () => {
  const [opened, setOpened] = useState(false);

  return (
    <Popover
      width={460}
      position="bottom-end"
      withArrow
      shadow="md"
      opened={opened}
      onChange={setOpened}
    >
      <Popover.Target>
        <Anchor
          component="button"
          type="button"
          size="xs"
          c="dimmed"
          onClick={() => setOpened((o) => !o)}
        >
          <IconHelp size={13} style={{ verticalAlign: "-2px" }} /> filter syntax
        </Anchor>
      </Popover.Target>

      <Popover.Dropdown>
        <Stack gap="sm">
          <Text size="xs">
            Separate filters with a space; a target has to match all of them.
          </Text>
          <Code block>labels.job=node health=down</Code>

          <Text size="xs" fw={600} mt="xs">
            By label
          </Text>
          <Text size="xs" c="dimmed">
            Every label needs the <Code>labels.</Code> prefix, including{" "}
            <Code>instance</Code> and <Code>job</Code>. Expand a row to see
            which labels a target carries.
          </Text>
          <Rows rows={labelRows} />

          <Text size="xs" fw={600} mt="xs">
            By target
          </Text>
          <Rows rows={fieldRows} />

          <Text size="xs" c="dimmed" mt="xs">
            Values are regular expressions, unquoted. <Code>=</Code> has to
            match the whole value, so use <Code>=~</Code> for a substring
            rather than wrapping it in <Code>.*</Code>.
          </Text>
        </Stack>
      </Popover.Dropdown>
    </Popover>
  );
};
