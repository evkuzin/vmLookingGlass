import { Badge } from "@mantine/core";
import type { FC } from "react";

const healthColor = (health: string) => {
  switch (health) {
    case "up":
      return "green";
    case "down":
      return "red";
    default:
      return "gray";
  }
};

export const HealthBadge: FC<{ health: string }> = ({ health }) => (
  <Badge color={healthColor(health)} variant="light">
    {health}
  </Badge>
);
