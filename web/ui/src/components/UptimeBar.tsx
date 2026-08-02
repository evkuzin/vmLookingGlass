import { Box, Group, Text, Tooltip } from "@mantine/core";
import type { FC } from "react";

import type { PollRecord } from "../api/types";
import { formatRelative } from "../lib/format";

// The bar is capped so a long history stays readable; older polls fall off
// the left rather than shrinking every segment to nothing.
const maxSegments = 60;

type UptimeBarProps = {
  history?: PollRecord[];
};

// UptimeBar shows recent poll outcomes as a strip, newest on the right.
// History is in-memory only, so it starts empty after a restart and fills as
// polls happen.
export const UptimeBar: FC<UptimeBarProps> = ({ history }) => {
  if (!history || history.length === 0) {
    return (
      <Text size="xs" c="dimmed">
        no history yet
      </Text>
    );
  }

  const shown = history.slice(-maxSegments);
  const failures = history.filter((h) => !h.ok).length;
  const pct = ((history.length - failures) / history.length) * 100;
  const span = formatRelative(history[0].at);

  return (
    <Tooltip
      withArrow
      label={`${pct.toFixed(1)}% of ${history.length} polls succeeded, oldest ${span}`}
    >
      <Group gap={1} wrap="nowrap" align="center">
        {shown.map((h, i) => (
          <Box
            key={`${h.at}-${i}`}
            w={3}
            h={16}
            style={{
              backgroundColor: h.ok
                ? "var(--mantine-color-green-6)"
                : "var(--mantine-color-red-6)",
              borderRadius: 1,
            }}
          />
        ))}
      </Group>
    </Tooltip>
  );
};
