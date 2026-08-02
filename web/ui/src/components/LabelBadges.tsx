import { Badge, Group } from "@mantine/core";
import type { FC } from "react";
import type { Labels } from "../api/types";

type LabelBadgesProps = {
  labels: Labels;
  variant?: "default" | "discovered";
};

// LabelBadges renders a label set as one badge per pair, sorted by name so
// the same target always reads the same way.
export const LabelBadges: FC<LabelBadgesProps> = ({ labels, variant = "default" }) => (
  <Group gap="xs" wrap="wrap">
    {Object.entries(labels)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, value]) => (
        <Badge
          key={name}
          variant="light"
          color={variant === "discovered" ? "blue" : "gray"}
          styles={{
            label: { textTransform: "none", fontWeight: 400 },
          }}
        >
          {name}=&quot;{value}&quot;
        </Badge>
      ))}
  </Group>
);
