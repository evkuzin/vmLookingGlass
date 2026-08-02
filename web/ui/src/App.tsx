import { AppShell, Container, Group, Tabs, Text, Title } from "@mantine/core";
import { IconServer2, IconTarget } from "@tabler/icons-react";
import type { FC } from "react";
import { StringParam, useQueryParam, withDefault } from "use-query-params";

import { ThemeToggle } from "./components/ThemeToggle";
import { AgentsPage } from "./pages/AgentsPage";
import { TargetsPage } from "./pages/TargetsPage";

export const App: FC = () => {
  const [tab, setTab] = useQueryParam("tab", withDefault(StringParam, "targets"));

  return (
    <AppShell header={{ height: 56 }} padding="md">
      <AppShell.Header>
        <Container size="xl" h="100%">
          <Group h="100%" justify="space-between">
            <Group gap="xs">
              <Title order={4}>vmLookingGlass</Title>
              <Text size="xs" c="dimmed">
                scrape targets across every agent
              </Text>
            </Group>
            <Group gap="sm">
              <Tabs value={tab} onChange={(v) => setTab(v ?? "targets")} variant="pills">
                <Tabs.List>
                  <Tabs.Tab value="targets" leftSection={<IconTarget size={14} />}>
                    Targets
                  </Tabs.Tab>
                  <Tabs.Tab value="agents" leftSection={<IconServer2 size={14} />}>
                    Agents
                  </Tabs.Tab>
                </Tabs.List>
              </Tabs>
              <ThemeToggle />
            </Group>
          </Group>
        </Container>
      </AppShell.Header>

      <AppShell.Main>
        <Container size="xl">
          {tab === "agents" ? <AgentsPage /> : <TargetsPage />}
        </Container>
      </AppShell.Main>
    </AppShell>
  );
};
