import { Group, Table, Text, UnstyledButton } from "@mantine/core";
import { IconChevronDown, IconChevronUp, IconSelector } from "@tabler/icons-react";
import type { FC } from "react";

// SortColumn describes one column. A column without a value is not sortable,
// which is the case for free-text fields like an error message.
export type SortColumn<T> = {
  key: string;
  label: string;
  value?: (row: T) => number | string;
  // descFirst is set where the interesting end is the largest, so one click
  // shows what matters instead of two.
  descFirst?: boolean;
};

export type SortState = {
  by: string;
  desc: boolean;
};

// nextSort returns the state after clicking a column: reversing it if already
// sorted by it, otherwise switching to it in its natural direction.
export function nextSort<T>(current: SortState, col: SortColumn<T>): SortState {
  if (col.key === current.by) {
    return { by: current.by, desc: !current.desc };
  }
  return { by: col.key, desc: col.descFirst ?? false };
}

// sortRows orders rows by the given column. Ties break on the first column so
// the order is stable across refreshes rather than shuffling equal rows.
export function sortRows<T>(
  rows: T[],
  columns: SortColumn<T>[],
  sort: SortState,
  tiebreak: (row: T) => string,
): T[] {
  const col = columns.find((c) => c.key === sort.by);
  if (!col?.value) {
    return rows;
  }
  const value = col.value;

  // Sorting a copy leaves the query cache entry untouched.
  return [...rows].sort((x, y) => {
    const a = value(x);
    const b = value(y);
    let cmp: number;
    if (typeof a === "number" && typeof b === "number") {
      cmp = a - b;
    } else {
      cmp = String(a).localeCompare(String(b));
    }
    if (cmp !== 0) {
      return sort.desc ? -cmp : cmp;
    }
    return tiebreak(x).localeCompare(tiebreak(y));
  });
}

type SortHeaderProps<T> = {
  col: SortColumn<T>;
  sort: SortState;
  onSort: (col: SortColumn<T>) => void;
};

export function SortHeader<T>({ col, sort, onSort }: SortHeaderProps<T>) {
  if (!col.value) {
    return <Table.Th>{col.label}</Table.Th>;
  }
  const active = sort.by === col.key;
  const Icon = active ? (sort.desc ? IconChevronDown : IconChevronUp) : IconSelector;

  return (
    <Table.Th>
      <UnstyledButton onClick={() => onSort(col)} style={{ width: "100%" }}>
        <Group gap={4} wrap="nowrap">
          <Text size="sm" fw={active ? 700 : 500}>
            {col.label}
          </Text>
          <Icon size={13} opacity={active ? 1 : 0.4} />
        </Group>
      </UnstyledButton>
    </Table.Th>
  );
}

// SortHeaders renders a full header row.
export function SortHeaders<T>({
  columns,
  sort,
  onSort,
  leading,
}: {
  columns: SortColumn<T>[];
  sort: SortState;
  onSort: (col: SortColumn<T>) => void;
  leading?: FC;
}) {
  const Leading = leading;
  return (
    <Table.Tr>
      {Leading && <Leading />}
      {columns.map((c) => (
        <SortHeader key={c.key} col={c} sort={sort} onSort={onSort} />
      ))}
    </Table.Tr>
  );
}
